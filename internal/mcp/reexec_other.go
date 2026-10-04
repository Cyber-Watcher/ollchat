//go:build !linux

package mcp

import (
	"context"
	"errors"
	"os"
)

// WatchBinary вне Linux не следит: канал nil не закрывается никогда.
func WatchBinary(ctx context.Context, srv *Server) <-chan struct{} { return nil }

// Reexec вне Linux не поддержан.
func Reexec() error {
	return errors.New("перезапуск на новом бинаре поддержан только в Linux")
}

// serveStdio — вне Linux без слежки за бинарём: подмена через exec
// и опрос потока здесь не проверялись (reexec_linux.go).
func serveStdio(ctx context.Context, srv *Server, verbose bool) error {
	return Serve(ctx, srv, os.Stdin, os.Stdout, verbose)
}
