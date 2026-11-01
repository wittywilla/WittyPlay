// Command wittyplay-module is WittyPlay's service: the only way a server reaches it. It holds the
// games being played and their streams, and starts a game runner for each. It runs while it's in
// use, and listens on this machine only. See package service in WittyModule.
package main

import (
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/rpc"
)

func main() {
	service.Main(service.Config{
		Manifest:  wittyplay.ManifestJSON,
		Handlers:  rpc.Handlers,
		ErrorCode: rpc.ErrorCode,
		Routes:    rpc.Routes,
		Busy:      rpc.Busy,
		OnStop:    rpc.Stop,
	})
}
