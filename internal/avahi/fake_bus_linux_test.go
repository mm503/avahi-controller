//go:build linux

package avahi

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/godbus/dbus/v5"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// This bus speaks the actual authentication and message protocols; no daemon
// or host system bus is needed, and the production ConnectSystemBus path runs.
func fakeBus(t *testing.T, mode string) <-chan *dbus.Message {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().(*net.TCPAddr)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", fmt.Sprintf("tcp:host=127.0.0.1,port=%d", addr.Port))
	requests := make(chan *dbus.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			conn, err := listener.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return err
			}
			reader := bufio.NewReader(conn)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return err
				}
				line = strings.TrimSpace(strings.TrimPrefix(line, "\x00"))
				var response string
				switch {
				case line == "AUTH":
					response = "REJECTED EXTERNAL\r\n"
				case line == "AUTH EXTERNAL":
					response = "DATA\r\n"
				case strings.HasPrefix(line, "DATA"):
					response = "OK 0123456789abcdef0123456789abcdef\r\n"
				case line == "BEGIN":
					goto authenticated
				default:
					return fmt.Errorf("unexpected authentication line %q", line)
				}
				if _, err := io.WriteString(conn, response); err != nil {
					return err
				}
			}
		authenticated:
			reply := func(request *dbus.Message, typ dbus.Type, body any) error {
				headers := map[dbus.HeaderField]dbus.Variant{
					dbus.FieldReplySerial: dbus.MakeVariant(request.Serial()),
					dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(body)),
				}
				if typ == dbus.TypeError {
					headers[dbus.FieldErrorName] = dbus.MakeVariant("org.freedesktop.systemd1.Failed")
				}
				return (&dbus.Message{Type: typ, Headers: headers, Body: []any{body}}).EncodeTo(conn, binary.LittleEndian)
			}
			hello, err := dbus.DecodeMessage(reader)
			if err != nil {
				return err
			}
			if hello.Headers[dbus.FieldMember].Value() != "Hello" {
				return fmt.Errorf("expected Hello")
			}
			if err := reply(hello, dbus.TypeMethodReply, ":1.1"); err != nil {
				return err
			}
			request, err := dbus.DecodeMessage(reader)
			if err != nil {
				return err
			}
			requests <- request
			switch mode {
			case "success":
				err = reply(request, dbus.TypeMethodReply, dbus.ObjectPath("/org/freedesktop/systemd1/job/1"))
			case "bad-reply":
				err = reply(request, dbus.TypeMethodReply, true)
			case "error":
				err = reply(request, dbus.TypeError, "reload denied")
			case "silent":
			}
			if err != nil {
				return err
			}
			_, err = reader.ReadByte() // client closes after its reply or deadline
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}()
	}()
	t.Cleanup(func() {
		listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("fake bus: %v", err)
			}
		case <-time.After(6 * time.Second):
			t.Error("fake bus did not stop")
		}
	})
	return requests
}

func TestSystemdReloader_CallsReloadUnitReplace(t *testing.T) {
	requests := fakeBus(t, "success")
	r := &SystemdReloader{ServiceName: "custom.service"}
	if err := r.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	msg := <-requests
	if msg.Headers[dbus.FieldDestination].Value() != systemdBusName || msg.Headers[dbus.FieldPath].Value() != dbus.ObjectPath(systemdObjectPath) || msg.Headers[dbus.FieldInterface].Value() != systemdInterface || msg.Headers[dbus.FieldMember].Value() != "ReloadUnit" {
		t.Fatalf("wrong D-Bus target: %v", msg.Headers)
	}
	if len(msg.Body) != 2 || msg.Body[0] != "custom.service" || msg.Body[1] != "replace" {
		t.Fatalf("arguments: %v", msg.Body)
	}
}

func TestSystemdReloader_HonoursContextDeadline(t *testing.T) {
	requests := fakeBus(t, "silent")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := (&SystemdReloader{ServiceName: DefaultServiceName}).Reload(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	select {
	case <-requests:
	default:
		t.Fatal("deadline expired before reload call")
	}
}

func TestSystemdReloader_RejectsFailedOrMalformedReply(t *testing.T) {
	for _, mode := range []string{"error", "bad-reply"} {
		t.Run(mode, func(t *testing.T) {
			fakeBus(t, mode)
			if err := (&SystemdReloader{ServiceName: DefaultServiceName}).Reload(context.Background()); err == nil {
				t.Fatal("invalid reply accepted")
			}
		})
	}
}
