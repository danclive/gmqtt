package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/danclive/mqtt"
	"github.com/danclive/mqtt/plugin/management"
	"github.com/danclive/mqtt/plugin/prometheus"
)

func main() {
	// listener
	ln, err := net.Listen("tcp", ":1883")
	if err != nil {
		log.Fatalln(err.Error())
		return
	}
	ws := &mqtt.WsServer{
		Server: &http.Server{Addr: ":8080"},
		Path:   "/ws",
	}
	if err != nil {
		panic(err)
	}

	l, _ := zap.NewProduction()
	//l, _ := zap.NewDevelopment()
	s := mqtt.NewServer(
		mqtt.WithTCPListener(ln),
		mqtt.WithWebsocketServer(ws),
		mqtt.WithPlugin(management.New(":8081", nil)),
		mqtt.WithPlugin(prometheus.New(&http.Server{
			Addr: ":8082",
		}, "/metrics")),
		mqtt.WithLogger(l),
	)
	s.Run()
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	<-signalCh
	s.Stop(context.Background())

}
