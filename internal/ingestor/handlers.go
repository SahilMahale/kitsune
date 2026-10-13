package ingestor

import (
	"fmt"
	"time"

	"github.com/SahilMahale/kitsune/internal/models"
	"github.com/gofiber/fiber/v3"
)

var validLevels = map[string]bool{
	"INFO": true, "WARN": true, "ERROR": true, "DEBUG": true,
}

var validMetricTypes = map[string]bool{
	"gauge": true, "counter": true, "histogram": true,
}

// IngestLogsHandler handles POST /ingest/logs.
// Validates the payload, normalizes defaults, and publishes to kitsune.logs.raw.
func IngestLogsHandler(fCtx fiber.Ctx) error {
	var req models.IngestLogsRequest
	if err := fCtx.Bind().Body(&req); err != nil {
		return fCtx.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid JSON body",
		})
	}

	if len(req.Logs) == 0 {
		return fCtx.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "logs array must not be empty",
		})
	}

	for i := range req.Logs {
		entry := &req.Logs[i]

		if entry.Service == "" {
			return fCtx.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("logs[%d]: service is required", i),
			})
		}
		if entry.Message == "" {
			return fCtx.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("logs[%d]: message is required", i),
			})
		}
		if !validLevels[entry.Level] {
			return fCtx.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("logs[%d]: level must be INFO, WARN, ERROR, or DEBUG", i),
			})
		}
		if entry.Timestamp.IsZero() {
			entry.Timestamp = time.Now().UTC()
		}
		if entry.Source == "" {
			entry.Source = "kitsune"
		}
	}

	// TODO: publish req.Logs to Kafka topic kitsune.logs.raw

	return fCtx.Status(fiber.StatusAccepted).JSON(models.IngestResponse{
		Accepted: len(req.Logs),
		Status:   "accepted",
	})
}

// IngestKubeMetricsHandler handles POST /ingest/metrics/kube.
// Validates the payload, normalizes defaults, and publishes to kitsune.metrics.kube.
func IngestKubeMetricsHandler(fCtx fiber.Ctx) error {
	var req models.IngestMetricsRequest
	if err := fCtx.Bind().Body(&req); err != nil {
		return fCtx.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid JSON body",
		})
	}

	if len(req.Metrics) == 0 {
		return fCtx.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "metrics array must not be empty",
		})
	}

	for i := range req.Metrics {
		m := &req.Metrics[i]

		if m.Name == "" {
			return fCtx.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("metrics[%d]: name is required", i),
			})
		}
		if !validMetricTypes[m.Type] {
			return fCtx.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": fmt.Sprintf("metrics[%d]: type must be gauge, counter, or histogram", i),
			})
		}
		if m.Timestamp.IsZero() {
			m.Timestamp = time.Now().UTC()
		}
		if m.Source == "" {
			m.Source = "kube"
		}
	}

	// TODO: publish req.Metrics to Kafka topic kitsune.metrics.kube

	return fCtx.Status(fiber.StatusAccepted).JSON(models.IngestResponse{
		Accepted: len(req.Metrics),
		Status:   "accepted",
	})
}
