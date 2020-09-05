package httpproxy

import (
	"context"
	"io"
)

func tunnel(ctx context.Context, c1, c2 io.ReadWriteCloser) error {
	ctx, cancel := context.WithCancel(ctx)
	var errs tunnelErr
	go func() {
		var buf [32 * 1024]byte
		_, errs[0] = io.CopyBuffer(c1, c2, buf[:])
		cancel()
	}()
	go func() {
		var buf [32 * 1024]byte
		_, errs[1] = io.CopyBuffer(c2, c1, buf[:])
		cancel()
	}()
	<-ctx.Done()
	errs[2] = c1.Close()
	errs[3] = c2.Close()
	errs[4] = ctx.Err()
	if errs[4] == context.Canceled {
		errs[4] = nil
	}
	return errs.FirstError()
}

type tunnelErr [5]error

func (t tunnelErr) FirstError() error {
	for _, err := range t {
		if err != nil {
			return err
		}
	}
	return nil
}
