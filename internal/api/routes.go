package api

import (
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/healthcheck"
)

func RegisterEndpoints(server *fiber.App){
server.Get("/",healthcheck.New())
server.Get("/status",healthcheck.New())
server.Get("/health",healthcheck.New())
apiV1:= server.Group("/api/v1",)
apiV1.Post("/execute",ExecuteHander)
}
