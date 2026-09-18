package decorators

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strings"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/runtime/isolation"
)

type IsolatedTransportDecorator struct{}

var _ decorator.Transport = (*IsolatedTransportDecorator)(nil)

func (d *IsolatedTransportDecorator) Descriptor() decorator.Descriptor {
	return decorator.NewDescriptor("isolated").
		Summary("Execute in an isolated transport context").
		Roles(decorator.RoleBoundary).
		ParamEnum("level", "Isolation level").
		Values("none", "basic", "standard", "maximum").
		Default("standard").
		Done().
		ParamEnum("network", "Network isolation policy").
		Values("allow", "deny", "loopback").
		Default("allow").
		Done().
		Block(decorator.BlockRequired).
		Build()
}

func (d *IsolatedTransportDecorator) Capabilities() decorator.TransportCaps {
	return decorator.TransportCapNetwork |
		decorator.TransportCapFilesystem |
		decorator.TransportCapEnvironment |
		decorator.TransportCapIsolation
}

type isolatedConfig struct {
	Level   string `decorator:"level"`
	Network string `decorator:"network"`
}

func (d *IsolatedTransportDecorator) Open(parent decorator.Session, params map[string]any) (decorator.Session, error) {
	cfg, _, err := decorator.DecodeInto[isolatedConfig](
		d.Descriptor().Schema,
		nil,
		params,
	)
	if err != nil {
		return nil, err
	}

	level := decorator.IsolationLevelStandard
	switch cfg.Level {
	case "none":
		level = decorator.IsolationLevelNone
	case "basic":
		level = decorator.IsolationLevelBasic
	case "standard":
		level = decorator.IsolationLevelStandard
	case "maximum":
		level = decorator.IsolationLevelMaximum
	}

	networkPolicy := decorator.NetworkPolicyAllow
	switch cfg.Network {
	case "deny":
		networkPolicy = decorator.NetworkPolicyDeny
	case "loopback":
		networkPolicy = decorator.NetworkPolicyLoopbackOnly
	}

	config := decorator.IsolationConfig{
		NetworkPolicy:    networkPolicy,
		FilesystemPolicy: decorator.FilesystemPolicyFull,
		MemoryLock:       false,
	}

	isolator := isolation.NewIsolator()
	if err := isolator.Isolate(level, config); err != nil {
		return nil, fmt.Errorf("failed to create isolated environment: %w", err)
	}

	return &isolatedSession{parent: parent, isolator: isolator, networkPolicy: networkPolicy}, nil
}

func (d *IsolatedTransportDecorator) Wrap(next decorator.ExecNode, params map[string]any) decorator.ExecNode {
	return next
}

func (d *IsolatedTransportDecorator) MaterializeSession() bool {
	return true
}

func (d *IsolatedTransportDecorator) IsolationContext() decorator.IsolationContext {
	return isolation.NewIsolator()
}

type isolatedSession struct {
	parent        decorator.Session
	isolator      decorator.IsolationContext
	networkPolicy decorator.NetworkPolicy
}

var (
	_        decorator.Session               = (*isolatedSession)(nil)
	_        decorator.NetworkDialer         = (*isolatedSession)(nil)
	_        decorator.NetworkDialerProvider = (*isolatedSession)(nil)
	lookupIP                                 = func(ctx context.Context, host string) ([]net.IP, error) {
		var resolver net.Resolver
		addrs, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}

		ips := make([]net.IP, len(addrs))
		for i, addr := range addrs {
			ips[i] = addr.IP
		}

		return ips, nil
	}
)

func (s *isolatedSession) Run(ctx context.Context, argv []string, opts decorator.RunOpts) (decorator.Result, error) {
	return s.parent.Run(ctx, argv, opts)
}

func (s *isolatedSession) OpenFileOutput(ctx context.Context, path string, mode decorator.FileWriteMode, perm fs.FileMode) (decorator.Output, error) {
	return decorator.OpenFileOutput(ctx, s.parent, path, mode, perm)
}

func (s *isolatedSession) Put(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	return s.parent.Put(ctx, data, path, mode)
}

func (s *isolatedSession) Get(ctx context.Context, path string) ([]byte, error) {
	return s.parent.Get(ctx, path)
}

func (s *isolatedSession) Env() map[string]string {
	return s.parent.Env()
}

func (s *isolatedSession) WithEnv(delta map[string]string) decorator.Session {
	return &isolatedSession{parent: s.parent.WithEnv(delta), isolator: s.isolator, networkPolicy: s.networkPolicy}
}

func (s *isolatedSession) WithWorkdir(dir string) decorator.Session {
	return &isolatedSession{parent: s.parent.WithWorkdir(dir), isolator: s.isolator, networkPolicy: s.networkPolicy}
}

func (s *isolatedSession) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	validatedAddr, err := validateNetworkPolicy(ctx, s.networkPolicy, network, addr)
	if err != nil {
		return nil, err
	}

	dialer, err := decorator.GetNetworkDialer(s.parent)
	if err != nil {
		return nil, fmt.Errorf("inner session does not support networking: %w", err)
	}

	return dialer.DialContext(ctx, network, validatedAddr)
}

func (s *isolatedSession) NetworkDialer() decorator.NetworkDialer {
	return s
}

func validateNetworkPolicy(ctx context.Context, policy decorator.NetworkPolicy, network, addr string) (string, error) {
	switch policy {
	case decorator.NetworkPolicyAllow:
		return addr, nil
	case decorator.NetworkPolicyDeny:
		return "", errors.New("network access denied by isolation policy")
	case decorator.NetworkPolicyLoopbackOnly:
		if network == "unix" || network == "unixgram" || network == "unixpacket" {
			return addr, nil
		}

		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return "", fmt.Errorf("invalid network address %q: %w", addr, err)
		}

		host = strings.TrimSpace(strings.Trim(host, "[]"))
		if host == "" {
			return "", fmt.Errorf("network access denied by isolation policy: %s", addr)
		}

		ips, err := lookupIP(ctx, host)
		if err != nil {
			return "", fmt.Errorf("network access denied by isolation policy: %s", addr)
		}
		if len(ips) == 0 {
			return "", fmt.Errorf("network access denied by isolation policy: %s", addr)
		}

		for _, ip := range ips {
			if !ip.IsLoopback() {
				return "", fmt.Errorf("network access denied by isolation policy: %s resolves to non-loopback IP %s", addr, ip.String())
			}
		}

		return net.JoinHostPort(ips[0].String(), port), nil
	default:
		return "", errors.New("network access denied by isolation policy")
	}
}

func (s *isolatedSession) Cwd() string {
	return s.parent.Cwd()
}

func (s *isolatedSession) Platform() string {
	return s.parent.Platform()
}

func (s *isolatedSession) ID() string {
	return s.parent.ID() + "/isolated"
}

func (s *isolatedSession) TransportScope() decorator.TransportScope {
	return decorator.TransportScopeIsolated
}

func (s *isolatedSession) Close() error {
	return nil
}
