package api

import "github.com/gofiber/fiber/v3"

func ExecuteHander(fCtx fiber.Ctx) error{
	return fCtx.SendString("Hi for now more to come!! :)")
}

