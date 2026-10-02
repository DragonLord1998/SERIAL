//go:build windows

package desktop

import (
	"context"
	"github.com/Microsoft/go-winio"
	"net"
)

func dialPlayer(ctx context.Context, path string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, path)
}
