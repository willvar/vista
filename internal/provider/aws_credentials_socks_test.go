package provider

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"vista/internal/network"
)

func TestAWSSOCKSReadTimeoutAndCancellation(t *testing.T) {
	for _, route := range []string{"remote-dns", "pinned"} {
		for _, outcome := range []string{"timeout", "cancel"} {
			t.Run(route+"/"+outcome, func(t *testing.T) {
				profile, closed := awsStalledSOCKSFixture(t)
				policy := network.Policy{Profile: &profile}
				ctx, cancel := context.WithTimeout(network.WithConnectionPolicy(context.Background(), policy), 2*time.Second)
				defer cancel()
				endpoint := "http://198.51.100.1/stalled"
				if route == "pinned" {
					var err error
					ctx, err = network.WithAddresses(ctx, "credentials.invalid", []net.IP{net.ParseIP("198.51.100.1")})
					if err != nil {
						t.Fatal(err)
					}
					endpoint = "http://credentials.invalid/stalled"
				}
				readTimeout := 200 * time.Millisecond
				if outcome == "cancel" {
					readTimeout = time.Minute
				}
				client, err := awsCredentialHTTPClient(ctx, awshttp.NewBuildableClient().WithReadTimeout(readTimeout))
				if err != nil {
					t.Fatal(err)
				}
				defer client.CloseIdleConnections()
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
				response, err := client.Do(request)
				if err != nil {
					t.Fatalf("SOCKS fixture did not reach its stalled response: %v", err)
				}
				if outcome == "cancel" {
					cancel()
				}
				_, err = io.ReadAll(response.Body)
				response.Body.Close()
				if outcome == "cancel" {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("operation cancellation lost: %v", err)
					}
				} else {
					var timeout *awshttp.ResponseTimeoutError
					if !errors.As(err, &timeout) || timeout.TimeoutDur != readTimeout || ctx.Err() != nil {
						t.Fatalf("SDK read timeout lost on %s: %v", route, err)
					}
				}
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("failed/cancelled SOCKS response left its connection open")
				}
			})
		}
	}
}

// The fixture negotiates SOCKS and returns HTTP headers without a body. It
// never opens a target connection, even when the requested address is literal.
func awsStalledSOCKSFixture(t *testing.T) (network.Profile, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		stopClose := context.AfterFunc(ctx, func() { conn.Close() })
		defer stopClose()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		var greeting [2]byte
		if _, err := io.ReadFull(conn, greeting[:]); err != nil {
			return
		}
		if _, err := io.CopyN(io.Discard, conn, int64(greeting[1])); err != nil {
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			return
		}
		var request [4]byte
		if _, err := io.ReadFull(conn, request[:]); err != nil {
			return
		}
		var addressSize int64
		switch request[3] {
		case 1:
			addressSize = 4
		case 4:
			addressSize = 16
		case 3:
			var length [1]byte
			if _, err := io.ReadFull(conn, length[:]); err != nil {
				return
			}
			addressSize = int64(length[0])
		default:
			return
		}
		if _, err := io.CopyN(io.Discard, conn, addressSize+2); err != nil {
			return
		}
		if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
			return
		}
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			return
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\n"); err != nil {
			return
		}
		io.Copy(io.Discard, conn)
	}()
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return network.Profile{ID: "socks-fixture", Name: "SOCKS fixture", Protocol: "socks5", Server: "127.0.0.1", Port: port, Authentication: "none"}, closed
}
