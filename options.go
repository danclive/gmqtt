package mqtt

import (
	"net"

	"go.uber.org/zap"
)

type Options func(srv *server)

// WithConfig set the config of the server
func WithConfig(config Config) Options {
	return func(srv *server) {
		srv.config = config
	}
}

// WithTCPListener set  tcp listener of the server. Default listen on  :1883.
func WithTCPListener(listener net.Listener) Options {
	return func(srv *server) {
		srv.tcpListener = append(srv.tcpListener, listener)
	}
}

// WithWebsocketServer set  websocket server(s) of the server.
func WithWebsocketServer(ws *WsServer) Options {
	return func(srv *server) {
		srv.websocketServer = append(srv.websocketServer, ws)
	}
}

// WithPlugin set plugin(s) of the server.
func WithPlugin(plugin Plugin) Options {
	return func(srv *server) {
		srv.plugins = append(srv.plugins, plugin)
	}
}

// WithHook set hooks of the server. Notice: WithPlugin() will overwrite hooks.
func WithHook(hooks Hooks) Options {
	return func(srv *server) {
		srv.hooks = append(srv.hooks, hooks)
	}
}

func WithLogger(logger *zap.Logger) Options {
	return func(srv *server) {
		zaplog = logger
	}
}
