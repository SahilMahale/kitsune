package ingestor

import (
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/healthcheck"
)

func RegisterEndpoints(server *fiber.App){
server.Get("/",healthcheck.New())
server.Get("/status",healthcheck.New())
server.Get("/health",healthcheck.New())
ingest:= server.Group("ingest",)
ingest.Post("/logs",PlaceHolderHandler)
ingest.Post("/metrics/kube",PlaceHolderHandler)
}
