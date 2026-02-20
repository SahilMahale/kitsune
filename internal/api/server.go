package api

import (
	"fmt"
	"os"

	"github.com/gofiber/fiber/v3/middleware/logger"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
)

func addLogger(server *fiber.App) {
	// Adding logger to the app
	server.Use(requestid.New())
	server.Use(logger.New(logger.Config{
		// For more options, see the Config section
		Format: "\n|${pid} ${locals:requestid} ${status} - ${method} ${path}|\n",
	}))
	server.Use(recoverer.New(recoverer.Config{EnableStackTrace: true}))
	server.Use(cors.New(cors.Config{
		AllowHeaders: []string{"Origin", "Content-Type","Accept", "Authorization"},
	}))
}
func Start() {

	bindAddr := os.Getenv("SERVER_BIND_ADDRESS")
	if bindAddr == "" {
		bindAddr = "0.0.0.0:8001"
	}
	server := fiber.New(fiber.Config{
		AppName:       "Kitsune",
		StrictRouting: true,
		ServerHeader:  "Kitsune_server",
	})

	server.Use(recoverer.New())
	addLogger(server)
	RegisterEndpoints(server)
	err:= server.Listen(bindAddr)
	if (err!=nil){
		fmt.Println(err)
		panic("Could Not start Kitsune server")
	}
}
