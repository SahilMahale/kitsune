package ingestor

import "github.com/gofiber/fiber/v3"

func PlaceHolderHandler(fCtx fiber.Ctx) error {
	return fCtx.SendString("Hi for now more to come!! :)")
}
