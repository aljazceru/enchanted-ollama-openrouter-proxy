package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
)

type orModels struct {
	Data []struct {
		ID            string `json:"id"`
		ContextLength int    `json:"context_length"`
		TopProvider   struct {
			ContextLength int `json:"context_length"`
		} `json:"top_provider"`
		Pricing struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing"`
	} `json:"data"`
}

func fetchFreeModels(apiKey string) ([]string, error) {
	req, err := http.NewRequest("GET", "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}
	var result orModels
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	type item struct {
		id  string
		ctx int
	}
	var items []item
	for _, m := range result.Data {
		if m.Pricing.Prompt == "0" && m.Pricing.Completion == "0" {
			ctx := m.TopProvider.ContextLength
			if ctx == 0 {
				ctx = m.ContextLength
			}
			items = append(items, item{id: m.ID, ctx: ctx})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ctx > items[j].ctx })
	models := make([]string, len(items))
	for i, it := range items {
		models[i] = it.id
	}
	return models, nil
}

func ensureFreeModelFile(apiKey, path string) ([]string, error) {
	if _, err := os.Stat(path); err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var models []string
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				models = append(models, line)
			}
		}
		return models, nil
	}
	models, err := fetchFreeModels(apiKey)
	if err != nil {
		return nil, err
	}
	_ = os.WriteFile(path, []byte(strings.Join(models, "\n")), 0644)
	return models, nil
}
