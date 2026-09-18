package decorator

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// SSHTestServer is a pure Go SSH server for testing.
type SSHTestServer struct {
	Port      int
	HostKey   ssh.Signer
	ClientKey ssh.Signer
	listener  net.Listener
	t         *testing.T
	wg        sync.WaitGroup
	env       map[string]string
}

// StartSSHTestServer creates and starts a pure Go SSH server.
// Returns nil if server cannot be started (tests will skip gracefully).
func StartSSHTestServer(t *testing.T) *SSHTestServer {
	t.Helper()

	// Generate ephemeral host key
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Skip("Failed to generate host key:", err)
		return nil
	}
	hostKey, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Skip("Failed to create host signer:", err)
		return nil
	}

	// Generate ephemeral client key
	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Skip("Failed to generate client key:", err)
		return nil
	}
	clientKey, err := ssh.NewSignerFromKey(clientPriv)
	if err != nil {
		t.Skip("Failed to create client signer:", err)
		return nil
	}

	// Convert client public key to SSH format
	clientSSHPub, err := ssh.NewPublicKey(clientPub)
	if err != nil {
		t.Skip("Failed to create SSH public key:", err)
		return nil
	}

	// Configure SSH server
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			// Accept only our test client key
			if bytes.Equal(key.Marshal(), clientSSHPub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unknown public key")
		},
	}
	config.AddHostKey(hostKey)

	// Listen on random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("Failed to listen:", err)
		return nil
	}

	port := listener.Addr().(*net.TCPAddr).Port

	// Capture current environment for test server
	env := make(map[string]string)
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}

	server := &SSHTestServer{
		Port:      port,
		HostKey:   hostKey,
		ClientKey: clientKey,
		listener:  listener,
		t:         t,
		env:       env,
	}

	// Start accepting connections
	server.wg.Add(1)
	go server.acceptLoop(config)

	// Note: Cleanup is NOT registered here to allow server reuse across tests
	// Caller is responsible for cleanup if needed

	return server
}

func (s *SSHTestServer) acceptLoop(config *ssh.ServerConfig) {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // Listener closed
		}

		s.wg.Add(1)
		go s.handleConn(conn, config)
	}
}

func (s *SSHTestServer) handleConn(netConn net.Conn, config *ssh.ServerConfig) {
	defer s.wg.Done()
	defer func() { _ = netConn.Close() }()

	// SSH handshake
	sshConn, chans, reqs, err := ssh.NewServerConn(netConn, config)
	if err != nil {
		return
	}
	defer func() { _ = sshConn.Close() }()

	// Discard global requests
	go ssh.DiscardRequests(reqs)

	// Handle channels (exec, shell, etc.)
	for newChannel := range chans {
		s.wg.Add(1)
		go s.handleChannel(newChannel)
	}
}

func (s *SSHTestServer) handleChannel(newChannel ssh.NewChannel) {
	defer s.wg.Done()

	if newChannel.ChannelType() != "session" {
		_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
		return
	}

	channel, requests, err := newChannel.Accept()
	if err != nil {
		return
	}
	defer func() { _ = channel.Close() }()

	// Handle session requests (exec, env, etc.)
	sessionEnv := make(map[string]string)
	for k, v := range s.env {
		sessionEnv[k] = v
	}

	for req := range requests {
		switch req.Type {
		case "exec":
			s.handleExec(channel, req, sessionEnv)
		case "env":
			s.handleEnv(req, sessionEnv)
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

func (s *SSHTestServer) handleEnv(req *ssh.Request, sessionEnv map[string]string) {
	// Parse env request: string name, string value
	var envReq struct {
		Name  string
		Value string
	}
	if err := ssh.Unmarshal(req.Payload, &envReq); err == nil {
		sessionEnv[envReq.Name] = envReq.Value
	}
	if req.WantReply {
		_ = req.Reply(true, nil)
	}
}

func (s *SSHTestServer) handleExec(channel ssh.Channel, req *ssh.Request, sessionEnv map[string]string) {
	// Parse command from request payload
	var execReq struct {
		Command string
	}
	if err := ssh.Unmarshal(req.Payload, &execReq); err != nil {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		_ = channel.Close()
		return
	}

	if req.WantReply {
		_ = req.Reply(true, nil)
	}

	// Execute command locally (for testing)
	cmd := exec.Command("sh", "-c", execReq.Command)

	// Set environment
	cmd.Env = make([]string, 0, len(sessionEnv))
	for k, v := range sessionEnv {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// Wire up I/O
	stdin, pipeErr := cmd.StdinPipe()
	if pipeErr != nil {
		_ = channel.Close()
		return
	}
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		defer stdin.Close()
		_, _ = io.Copy(stdin, channel)
	}()
	cmd.Stdout = channel
	cmd.Stderr = channel.Stderr()

	// Run command
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	// Send exit status
	exitStatus := struct{ Status uint32 }{uint32(exitCode)}
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&exitStatus))

	// Close the channel to signal completion
	_ = channel.Close()
	<-inputDone
}

// Stop stops the SSH server and waits for all connections to close.
func (s *SSHTestServer) Stop() {
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.wg.Wait()
}

// Addr returns the server address for connecting.
func (s *SSHTestServer) Addr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.Port)
}

// NewClientConfig returns an ssh.ClientConfig for connecting to this server.
func (s *SSHTestServer) NewClientConfig(user string) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(s.ClientKey),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
}

// Dial creates a new SSH client connection to this server.
func (s *SSHTestServer) Dial(user string) (*ssh.Client, error) {
	config := s.NewClientConfig(user)
	return ssh.Dial("tcp", s.Addr(), config)
}

// RunCommand is a helper to run a command and return output.
func (s *SSHTestServer) RunCommand(user, command string) (stdout, stderr string, exitCode int, err error) {
	client, err := s.Dial(user)
	if err != nil {
		return "", "", 1, err
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		return "", "", 1, err
	}
	defer func() { _ = session.Close() }()

	var outBuf, errBuf bytes.Buffer
	session.Stdout = &outBuf
	session.Stderr = &errBuf

	err = session.Run(command)
	if err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			exitCode = 1
		}
	}

	return outBuf.String(), errBuf.String(), exitCode, nil
}

// GetEnv returns the value of an environment variable on the server.
func (s *SSHTestServer) GetEnv(user, varName string) (string, error) {
	stdout, _, _, err := s.RunCommand(user, fmt.Sprintf("echo -n $%s", varName))
	if err != nil {
		return "", err
	}
	return stdout, nil
}

// WriteFile writes a file on the server (for testing file operations).
func (s *SSHTestServer) WriteFile(user, path, content string) error {
	client, err := s.Dial(user)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	session.Stdin = strings.NewReader(content)
	return session.Run(fmt.Sprintf("cat > %s", path))
}

// ReadFile reads a file from the server (for testing file operations).
func (s *SSHTestServer) ReadFile(user, path string) (string, error) {
	stdout, _, _, err := s.RunCommand(user, fmt.Sprintf("cat %s", path))
	return stdout, err
}
