package azure

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type flexibleSKU struct {
	Name   string
	Status string
}
type flexibleEdition struct {
	Name       string
	Status     string
	ServerSkus []flexibleSKU `json:"supportedServerSkus"`
	Skus       []flexibleSKU `json:"supportedSkus"`
}

func (s *Session) DatabaseSKUs(ctx context.Context, region, kind string) ([]string, error) {
	engine, ok := flexibleEngines[kind]
	if !ok {
		return nil, fmt.Errorf("unknown flexible server engine %s", kind)
	}
	suffix := "capabilities"
	if kind == "mysql" {
		suffix = "capabilitySets"
	}
	var out struct {
		Value []struct {
			Restricted string
			Editions   []flexibleEdition `json:"supportedServerEditions"`
			Properties struct {
				Editions []flexibleEdition `json:"supportedFlexibleServerEditions"`
			}
		}
	}
	path := s.subscription() + "/providers/" + engine.namespace + "/locations/" + region + "/" + suffix
	if _, err := s.do(ctx, http.MethodGet, path, engine.version, nil, &out); err != nil {
		return nil, err
	}
	var sizes []string
	for _, location := range out.Value {
		if strings.EqualFold(location.Restricted, "Enabled") {
			continue
		}
		for _, edition := range append(location.Editions, location.Properties.Editions...) {
			if !strings.EqualFold(edition.Name, "Burstable") || strings.EqualFold(edition.Status, "Disabled") {
				continue
			}
			for _, sku := range append(edition.ServerSkus, edition.Skus...) {
				if !strings.EqualFold(sku.Status, "Disabled") {
					sizes = append(sizes, sku.Name)
				}
			}
		}
	}
	return sizes, nil
}

func (s *Session) RedisScaleSizes(ctx context.Context, name string) ([]string, error) {
	var out struct {
		Skus []flexibleSKU `json:"skus"`
	}
	path := s.ManagedRedisPath(name) + "/listSkusForScaling"
	if _, err := s.do(ctx, http.MethodPost, path, "2026-09-01", nil, &out); err != nil {
		return nil, err
	}
	sizes := make([]string, 0, len(out.Skus))
	for _, sku := range out.Skus {
		sizes = append(sizes, sku.Name)
	}
	return sizes, nil
}
