//go:build !windows

package desktop

import (
	"context"
	"net"
)

func dialPlayer(ctx context.Context, path string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}
