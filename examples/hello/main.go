package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	platform "github.com/NARUBROWN/spine-fasthttp-platform"
	"github.com/NARUBROWN/spine/pkg/api"
	"github.com/NARUBROWN/spine/pkg/boot"
	"github.com/NARUBROWN/spine/pkg/constructor"
	"github.com/NARUBROWN/spine/pkg/httpx"
)

type HelloController struct{}

func (*HelloController) Constructor() *HelloController { return &HelloController{} }

func (*HelloController) RegisterRoutes(r *api.Registry) {

	r.GET("/hello", func() httpx.Response[string] {
		return httpx.Response[string]{Body: "hello"}
	})

}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := platform.New()
	app.Constructor(constructor.Controller(&HelloController{}))
	address := os.Getenv("SPINE_ADDRESS")
	if address == "" {
		address = ":8080"
	}
	if err := app.RunContext(ctx, boot.Options{Address: address, HTTP: &boot.HTTPOptions{}}); err != nil {
		log.Fatal(err)
	}
}
