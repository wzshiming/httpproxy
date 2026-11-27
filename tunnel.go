package httpproxy

import (
	"context"
	"errors"
	"io"
)

// errTunnelDone is used to stop the tunnel without waiting for the other direction.
var errTunnelDone = errors.New("tunnel done")

type closeWriter interface {
	CloseWrite() error
}

// tunnel create tunnels for two io.ReadWriteCloser.
// When one of them is done with sending, the other one is notified by
// shutting down the writing side of it if that is supported, and the
// data in the other direction is still relayed until that is done too.
func tunnel(ctx context.Context, c1, c2 io.ReadWriteCloser, buf1, buf2 []byte) error {
	errCh := make(chan error, 2)
	go func() {
		errCh <- relay(c1, c2, buf1)
	}()
	go func() {
		errCh <- relay(c2, c1, buf2)
	}()
	defer func() {
		_ = c1.Close()
		_ = c2.Close()
	}()

	for i := 0; i != 2; i++ {
		select {
		case err := <-errCh:
			if err == errTunnelDone {
				return nil
			}
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func relay(dst io.Writer, src io.Reader, buf []byte) error {
	_, err := io.CopyBuffer(dst, src, buf)
	if err != nil {
		return err
	}
	if cw, ok := dst.(closeWriter); ok && cw.CloseWrite() == nil {
		return nil
	}
	return errTunnelDone
}

// BytesPool is an interface for getting and returning temporary
// bytes for use by io.CopyBuffer.
type BytesPool interface {
	Get() []byte
	Put([]byte)
}
