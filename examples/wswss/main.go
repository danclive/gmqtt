package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/danclive/mqtt"
)

func main() {
	ln, err := net.Listen("tcp", ":1883")
	if err != nil {
		log.Fatalln(err.Error())
		return
	}

	ws := &mqtt.WsServer{
		Server: &http.Server{Addr: ":8080"},
		Path:   "/",
	}
	wss := &mqtt.WsServer{
		Server:   &http.Server{Addr: ":8081"},
		Path:     "/",
		CertFile: "../testcerts/server.crt",
		KeyFile:  "../testcerts/server.key",
	}
	s := mqtt.NewServer(
		mqtt.WithTCPListener(ln),
		mqtt.WithWebsocketServer(ws, wss),
	)
	s.Run()
	fmt.Println("started...")
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	<-signalCh
	s.Stop(context.Background())
	fmt.Println("stopped")

}
