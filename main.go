package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	openai "github.com/sashabaranov/go-openai"
)

var modelFilter map[string]struct{}
var freeModels []string
var failureStore *FailureStore
var freeMode bool

func loadModelFilter(path string) (map[string]struct{}, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	filter := make(map[string]struct{})

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			filter[line] = struct{}{}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return filter, nil
}

func main() {
	r := gin.Default()
	// Load the API key from environment variables.
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		slog.Error("OPENAI_API_KEY environment variable not set.")
		return
	}

	freeMode = strings.ToLower(os.Getenv("FREE_MODE")) != "false"

	if freeMode {
		var err error
		freeModels, err = ensureFreeModelFile(apiKey, "free-models")
		if err != nil {
			slog.Error("failed to load free models", "error", err)
			return
		}
		failureStore, err = NewFailureStore("failures.db")
		if err != nil {
			slog.Error("failed to init failure store", "error", err)
			return
		}
		defer failureStore.Close()
		slog.Info("Free mode enabled", "models", len(freeModels))
	}

	provider := NewOpenrouterProvider(apiKey)

	filter, err := loadModelFilter("/models-filter/filter")
	if err != nil {
		if os.IsNotExist(err) {
			slog.Info("models-filter file not found. Skipping model filtering.")
			modelFilter = make(map[string]struct{})
		} else {
			slog.Error("Error loading models filter", "Error", err)
			return
		}
	} else {
		modelFilter = filter
		slog.Info("Loaded models from filter:")
		for model := range modelFilter {
			slog.Info(" - " + model)
		}
	}

	r.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, "Ollama is running")
	})
	r.HEAD("/", func(c *gin.Context) {
		c.String(http.StatusOK, "")
	})

	r.GET("/api/tags", func(c *gin.Context) {
		var newModels []map[string]interface{}
		
		// Check if tool use filtering is enabled
		toolUseOnly := strings.ToLower(os.Getenv("TOOL_USE_ONLY")) == "true"

		if freeMode {
			// In free mode, show only available free models
			currentTime := time.Now().Format(time.RFC3339)
			for _, freeModel := range freeModels {
				// Check if model should be skipped due to recent failures
				skip, err := failureStore.ShouldSkip(freeModel)
				if err != nil {
					slog.Error("db error checking model", "model", freeModel, "error", err)
					continue
				}
				if skip {
					continue // Skip recently failed models
				}

				// Extract display name from full model name
				parts := strings.Split(freeModel, "/")
				displayName := parts[len(parts)-1]

				// Apply model filter if it exists
				if !isModelInFilter(displayName, modelFilter) {
					continue // Skip models not in filter
				}

				newModels = append(newModels, map[string]interface{}{
					"name":        displayName,
					"model":       displayName,
					"modified_at": currentTime,
					"size":        270898672,
					"digest":      "9077fe9d2ae1a4a41a868836b56b8163731a8fe16621397028c2c76f838c6907",
					"details": map[string]interface{}{
						"parent_model":       "",
						"format":             "gguf",
						"family":             "free",
						"families":           []string{"free"},
						"parameter_size":     "varies",
						"quantization_level": "Q4_K_M",
					},
				})
			}
		} else {
			// Non-free mode: use original logic
			if toolUseOnly {
				// If tool use filtering is enabled, we need to fetch full model details from OpenRouter
				req, err := http.NewRequest("GET", "https://openrouter.ai/api/v1/models", nil)
				if err != nil {
					slog.Error("Error creating request for models", "Error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))
				
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					slog.Error("Error fetching models from OpenRouter", "Error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				defer resp.Body.Close()
				
				if resp.StatusCode != http.StatusOK {
					slog.Error("Unexpected status from OpenRouter", "status", resp.Status)
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch models"})
					return
				}
				
				var result orModels
				if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
					slog.Error("Error decoding models response", "Error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				
				// Filter models based on tool use support and model filter
				currentTime := time.Now().Format(time.RFC3339)
				newModels = make([]map[string]interface{}, 0, len(result.Data))
				for _, m := range result.Data {
					if !supportsToolUse(m.SupportedParameters) {
						continue // Skip models that don't support tool use
					}
					
					// Extract display name from full model name
					parts := strings.Split(m.ID, "/")
					displayName := parts[len(parts)-1]
					
					// Apply model filter if it exists
					if !isModelInFilter(displayName, modelFilter) {
						continue // Skip models not in filter
					}
					
					newModels = append(newModels, map[string]interface{}{
						"name":        displayName,
						"model":       displayName,
						"modified_at": currentTime,
						"size":        270898672,
						"digest":      "9077fe9d2ae1a4a41a868836b56b8163731a8fe16621397028c2c76f838c6907",
						"details": map[string]interface{}{
							"parent_model":       "",
							"format":             "gguf",
							"family":             "tool-enabled",
							"families":           []string{"tool-enabled"},
							"parameter_size":     "varies",
							"quantization_level": "Q4_K_M",
						},
					})
				}
			} else {
				// Standard non-free mode: get all models from provider
				models, err := provider.GetModels()
				if err != nil {
					slog.Error("Error getting models", "Error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				filter := modelFilter
				newModels = make([]map[string]interface{}, 0, len(models))
				for _, m := range models {
					// Если фильтр пустой, значит пропускаем проверку и берём все модели
					if len(filter) > 0 {
						if _, ok := filter[m.Model]; !ok {
							continue
						}
					}
					newModels = append(newModels, map[string]interface{}{
						"name":        m.Name,
						"model":       m.Model,
						"modified_at": m.ModifiedAt,
						"size":        270898672,
						"digest":      "9077fe9d2ae1a4a41a868836b56b8163731a8fe16621397028c2c76f838c6907",
						"details":     m.Details,
					})
				}
			}
		}

		c.JSON(http.StatusOK, gin.H{"models": newModels})
	})

	r.POST("/api/show", func(c *gin.Context) {
		var request map[string]string
		if err := c.BindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON payload"})
			return
		}

		modelName := request["name"]
		if modelName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Model name is required"})
			return
		}

		details, err := provider.GetModelDetails(modelName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, details)
	})

	r.POST("/api/chat", func(c *gin.Context) {
		var request struct {
			Model    string                         `json:"model"`
			Messages []openai.ChatCompletionMessage `json:"messages"`
			Stream   *bool                          `json:"stream"` // Добавим поле Stream
		}

		// Parse the JSON request
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON payload"})
			return
		}

		// Определяем, нужен ли стриминг (по умолчанию true, если не указано для /api/chat)
		// ВАЖНО: Open WebUI может НЕ передавать "stream": true для /api/chat, подразумевая это.
		// Нужно проверить, какой запрос шлет Open WebUI. Если не шлет, ставим true.
		streamRequested := true
		if request.Stream != nil {
			streamRequested = *request.Stream
		}

		// Если стриминг не запрошен, нужно будет реализовать отдельную логику
		// для сбора полного ответа и отправки его одним JSON.
		// Пока реализуем только стриминг.
		if !streamRequested {
			var response openai.ChatCompletionResponse
			var fullModelName string
			var err error
			if freeMode {
				response, fullModelName, err = getFreeChatForModel(provider, request.Messages, request.Model)
				if err != nil {
					slog.Error("free mode failed", "error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
			} else {
				fullModelName, err = provider.GetFullModelName(request.Model)
				if err != nil {
					slog.Error("Error getting full model name", "Error", err)
					// Ollama returns 404 for invalid model names
					c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
					return
				}
				response, err = provider.Chat(request.Messages, fullModelName)
				if err != nil {
					slog.Error("Failed to get chat response", "Error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
			}

			// Format the response according to Ollama's format
			if len(response.Choices) == 0 {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "No response from model"})
				return
			}

			// Extract the content from the response
			content := ""
			if len(response.Choices) > 0 && response.Choices[0].Message.Content != "" {
				content = response.Choices[0].Message.Content
			}

			// Get finish reason, default to "stop" if not provided
			finishReason := "stop"
			if response.Choices[0].FinishReason != "" {
				finishReason = string(response.Choices[0].FinishReason)
			}

			// Create Ollama-compatible response
			ollamaResponse := map[string]interface{}{
				"model":      fullModelName,
				"created_at": time.Now().Format(time.RFC3339),
				"message": map[string]string{
					"role":    "assistant",
					"content": content,
				},
				"done":              true,
				"finish_reason":     finishReason,
				"total_duration":    response.Usage.TotalTokens * 10, // Approximate duration based on token count
				"load_duration":     0,
				"prompt_eval_count": response.Usage.PromptTokens,
				"eval_count":        response.Usage.CompletionTokens,
				"eval_duration":     response.Usage.CompletionTokens * 10, // Approximate duration based on token count
			}

			slog.Info("Used model", "model", fullModelName)

			c.JSON(http.StatusOK, ollamaResponse)
			return
		}

		slog.Info("Requested model", "model", request.Model)
		var stream *openai.ChatCompletionStream
		var fullModelName string
		var err error
		if freeMode {
			stream, fullModelName, err = getFreeStreamForModel(provider, request.Messages, request.Model)
			if err != nil {
				slog.Error("free mode failed", "error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		} else {
			fullModelName, err = provider.GetFullModelName(request.Model)
			if err != nil {
				slog.Error("Error getting full model name", "Error", err, "model", request.Model)
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			stream, err = provider.ChatStream(request.Messages, fullModelName)
			if err != nil {
				slog.Error("Failed to create stream", "Error", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
		slog.Info("Using model", "fullModelName", fullModelName)
		// Call ChatStream to get the stream
		if err != nil {
			slog.Error("Failed to create stream", "Error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer stream.Close() // Ensure stream closure

		// --- ИСПРАВЛЕНИЯ для NDJSON (Ollama-style) ---

		// Set headers CORRECTLY for Newline Delimited JSON
		c.Writer.Header().Set("Content-Type", "application/x-ndjson") // <--- КЛЮЧЕВОЕ ИЗМЕНЕНИЕ
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		// Transfer-Encoding: chunked устанавливается Gin автоматически

		w := c.Writer // Получаем ResponseWriter
		flusher, ok := w.(http.Flusher)
		if !ok {
			slog.Error("Expected http.ResponseWriter to be an http.Flusher")
			// Отправить ошибку клиенту уже сложно, т.к. заголовки могли уйти
			return
		}

		var lastFinishReason string

		// Stream responses back to the client
		for {
			response, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				// End of stream from the backend provider
				break
			}
			if err != nil {
				slog.Error("Backend stream error", "Error", err)
				// Попытка отправить ошибку в формате NDJSON
				// Ollama обычно просто обрывает соединение или шлет 500 перед этим
				errorMsg := map[string]string{"error": "Stream error: " + err.Error()}
				errorJson, _ := json.Marshal(errorMsg)
				fmt.Fprintf(w, "%s\n", string(errorJson)) // Отправляем ошибку + \n
				flusher.Flush()
				return
			}

			// Сохраняем причину остановки, если она есть в чанке
			if len(response.Choices) > 0 && response.Choices[0].FinishReason != "" {
				lastFinishReason = string(response.Choices[0].FinishReason)
			}

			// Build JSON response structure for intermediate chunks (Ollama chat format)
			responseJSON := map[string]interface{}{
				"model":      fullModelName,
				"created_at": time.Now().Format(time.RFC3339),
				"message": map[string]string{
					"role":    "assistant",
					"content": response.Choices[0].Delta.Content, // Может быть ""
				},
				"done": false, // Всегда false для промежуточных чанков
			}

			// Marshal JSON
			jsonData, err := json.Marshal(responseJSON)
			if err != nil {
				slog.Error("Error marshaling intermediate response JSON", "Error", err)
				return // Прерываем, так как не можем отправить данные
			}

			// Send JSON object followed by a newline
			fmt.Fprintf(w, "%s\n", string(jsonData)) // <--- ИЗМЕНЕНО: Формат NDJSON (JSON + \n)

			// Flush data to send it immediately
			flusher.Flush()
		}

		// --- Отправка финального сообщения (done: true) в стиле Ollama ---

		// Определяем причину остановки (если бэкенд не дал, ставим 'stop')
		// Ollama использует 'stop', 'length', 'content_filter', 'tool_calls'
		if lastFinishReason == "" {
			lastFinishReason = "stop"
		}

		// ВАЖНО: Замените nil на 0 для числовых полей статистики
		finalResponse := map[string]interface{}{
			"model":      fullModelName,
			"created_at": time.Now().Format(time.RFC3339),
			"message": map[string]string{
				"role":    "assistant",
				"content": "", // Пустой контент для финального сообщения
			},
			"done":              true,
			"finish_reason":     lastFinishReason, // Необязательно для /api/chat Ollama, но не вредит
			"total_duration":    0,
			"load_duration":     0,
			"prompt_eval_count": 0, // <--- ИЗМЕНЕНО: nil заменен на 0
			"eval_count":        0, // <--- ИЗМЕНЕНО: nil заменен на 0
			"eval_duration":     0,
		}

		finalJsonData, err := json.Marshal(finalResponse)
		if err != nil {
			slog.Error("Error marshaling final response JSON", "Error", err)
			return
		}

		// Отправляем финальный JSON-объект + newline
		fmt.Fprintf(w, "%s\n", string(finalJsonData)) // <--- ИЗМЕНЕНО: Формат NDJSON
		flusher.Flush()

		// ВАЖНО: Для NDJSON НЕТ 'data: [DONE]' маркера.
		// Клиент понимает конец потока по получению объекта с "done": true
		// и/или по закрытию соединения сервером (что Gin сделает автоматически после выхода из хендлера).

		// --- Конец исправлений ---
	})

	// Add OpenAI-compatible endpoint for tools like Goose
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		var request openai.ChatCompletionRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON payload"})
			return
		}

		slog.Info("OpenAI API request", "model", request.Model, "stream", request.Stream)

		if request.Stream {
			// Handle streaming request
			var stream *openai.ChatCompletionStream
			var fullModelName string
			var err error

			if freeMode {
				stream, fullModelName, err = getFreeStreamForModel(provider, request.Messages, request.Model)
				if err != nil {
					slog.Error("free mode streaming failed", "error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
					return
				}
			} else {
				fullModelName, err = provider.GetFullModelName(request.Model)
				if err != nil {
					slog.Error("Error getting full model name", "Error", err, "model", request.Model)
					c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": err.Error()}})
					return
				}
				stream, err = provider.ChatStream(request.Messages, fullModelName)
				if err != nil {
					slog.Error("Failed to create stream", "Error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
					return
				}
			}
			defer stream.Close()

			// Set headers for Server-Sent Events (OpenAI format)
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			c.Writer.Header().Set("Cache-Control", "no-cache")
			c.Writer.Header().Set("Connection", "keep-alive")

			w := c.Writer
			flusher, ok := w.(http.Flusher)
			if !ok {
				slog.Error("Expected http.ResponseWriter to be an http.Flusher")
				return
			}

			// Stream responses in OpenAI format
			for {
				response, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					// Send final [DONE] message
					fmt.Fprintf(w, "data: [DONE]\n\n")
					flusher.Flush()
					break
				}
				if err != nil {
					slog.Error("Stream error", "Error", err)
					break
				}

				// Convert to OpenAI response format
				openaiResponse := openai.ChatCompletionStreamResponse{
					ID:      "chatcmpl-" + fmt.Sprintf("%d", time.Now().Unix()),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   fullModelName,
					Choices: []openai.ChatCompletionStreamChoice{
						{
							Index: 0,
							Delta: openai.ChatCompletionStreamChoiceDelta{
								Content: response.Choices[0].Delta.Content,
							},
						},
					},
				}

				// Add finish reason if present
				if len(response.Choices) > 0 && response.Choices[0].FinishReason != "" {
					openaiResponse.Choices[0].FinishReason = response.Choices[0].FinishReason
				}

				jsonData, err := json.Marshal(openaiResponse)
				if err != nil {
					slog.Error("Error marshaling response", "Error", err)
					break
				}

				fmt.Fprintf(w, "data: %s\n\n", string(jsonData))
				flusher.Flush()
			}
		} else {
			// Handle non-streaming request
			var response openai.ChatCompletionResponse
			var fullModelName string
			var err error

			if freeMode {
				response, fullModelName, err = getFreeChatForModel(provider, request.Messages, request.Model)
				if err != nil {
					slog.Error("free mode failed", "error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
					return
				}
			} else {
				fullModelName, err = provider.GetFullModelName(request.Model)
				if err != nil {
					slog.Error("Error getting full model name", "Error", err)
					c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": err.Error()}})
					return
				}
				response, err = provider.Chat(request.Messages, fullModelName)
				if err != nil {
					slog.Error("Failed to get chat response", "Error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
					return
				}
			}

			// Return OpenAI-compatible response
			response.ID = "chatcmpl-" + fmt.Sprintf("%d", time.Now().Unix())
			response.Object = "chat.completion"
			response.Created = time.Now().Unix()
			response.Model = fullModelName

			slog.Info("Used model", "model", fullModelName)
			c.JSON(http.StatusOK, response)
		}
	})

	// Add OpenAI-compatible models endpoint
	r.GET("/v1/models", func(c *gin.Context) {
		var models []gin.H
		
		// Check if tool use filtering is enabled
		toolUseOnly := strings.ToLower(os.Getenv("TOOL_USE_ONLY")) == "true"

		if freeMode {
			// In free mode, show only available free models
			slog.Info("Free mode enabled for /v1/models", "totalFreeModels", len(freeModels), "filterSize", len(modelFilter))
			if len(freeModels) > 0 {
				slog.Info("Sample free models:", "first", freeModels[0], "count", min(len(freeModels), 3))
			}
			for _, freeModel := range freeModels {
				skip, err := failureStore.ShouldSkip(freeModel)
				if err != nil {
					slog.Error("db error checking model", "model", freeModel, "error", err)
					continue
				}
				if skip {
					continue
				}

				parts := strings.Split(freeModel, "/")
				displayName := parts[len(parts)-1]

				// Apply model filter if it exists
				if !isModelInFilter(displayName, modelFilter) {
					slog.Info("Skipping model not in filter", "displayName", displayName, "fullModel", freeModel)
					continue // Skip models not in filter
				}
				if len(modelFilter) > 0 {
					slog.Info("Model passed filter", "displayName", displayName, "fullModel", freeModel)
				}

				slog.Debug("Adding model to /v1/models", "model", displayName, "fullModel", freeModel)
				models = append(models, gin.H{
					"id":       displayName,
					"object":   "model",
					"created":  time.Now().Unix(),
					"owned_by": "openrouter",
				})
			}
		} else {
			// Non-free mode: get all models from provider
			if toolUseOnly {
				// If tool use filtering is enabled, we need to fetch full model details from OpenRouter
				req, err := http.NewRequest("GET", "https://openrouter.ai/api/v1/models", nil)
				if err != nil {
					slog.Error("Error creating request for models", "Error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
					return
				}
				req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))
				
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					slog.Error("Error fetching models from OpenRouter", "Error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
					return
				}
				defer resp.Body.Close()
				
				if resp.StatusCode != http.StatusOK {
					slog.Error("Unexpected status from OpenRouter", "status", resp.Status)
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "Failed to fetch models"}})
					return
				}
				
				var result orModels
				if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
					slog.Error("Error decoding models response", "Error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
					return
				}
				
				// Filter models based on tool use support and model filter
				for _, m := range result.Data {
					if !supportsToolUse(m.SupportedParameters) {
						continue // Skip models that don't support tool use
					}
					
					// Extract display name from full model name
					parts := strings.Split(m.ID, "/")
					displayName := parts[len(parts)-1]
					
					// Apply model filter if it exists
					if !isModelInFilter(displayName, modelFilter) {
						continue // Skip models not in filter
					}
					
					models = append(models, gin.H{
						"id":       displayName,
						"object":   "model",
						"created":  time.Now().Unix(),
						"owned_by": "openrouter",
					})
				}
			} else {
				// Standard non-free mode: get all models from provider
				providerModels, err := provider.GetModels()
				if err != nil {
					slog.Error("Error getting models", "Error", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
					return
				}

				for _, m := range providerModels {
					if len(modelFilter) > 0 {
						if _, ok := modelFilter[m.Model]; !ok {
							continue
						}
					}
					models = append(models, gin.H{
						"id":       m.Model,
						"object":   "model",
						"created":  time.Now().Unix(),
						"owned_by": "openrouter",
					})
				}
			}
		}

		slog.Info("Returning models response", "modelCount", len(models))
		c.JSON(http.StatusOK, gin.H{
			"object": "list",
			"data":   models,
		})
	})

	r.Run(":11434")
}

func getFreeChat(provider *OpenrouterProvider, msgs []openai.ChatCompletionMessage) (openai.ChatCompletionResponse, string, error) {
	var resp openai.ChatCompletionResponse
	for _, m := range freeModels {
		// Apply model filter if it exists
		parts := strings.Split(m, "/")
		displayName := parts[len(parts)-1]
		if !isModelInFilter(displayName, modelFilter) {
			continue // Skip models not in filter
		}

		skip, err := failureStore.ShouldSkip(m)
		if err != nil {
			slog.Error("db error", "error", err)
			continue
		}
		if skip {
			continue
		}
		resp, err = provider.Chat(msgs, m)
		if err != nil {
			slog.Warn("model failed", "model", m, "error", err)
			_ = failureStore.MarkFailure(m)
			continue
		}
		// Clear failure record on successful request
		_ = failureStore.ClearFailure(m)
		return resp, m, nil
	}
	return resp, "", fmt.Errorf("no free models available")
}

func getFreeStream(provider *OpenrouterProvider, msgs []openai.ChatCompletionMessage) (*openai.ChatCompletionStream, string, error) {
	for _, m := range freeModels {
		// Apply model filter if it exists
		parts := strings.Split(m, "/")
		displayName := parts[len(parts)-1]
		if !isModelInFilter(displayName, modelFilter) {
			continue // Skip models not in filter
		}

		skip, err := failureStore.ShouldSkip(m)
		if err != nil {
			slog.Error("db error", "error", err)
			continue
		}
		if skip {
			continue
		}
		stream, err := provider.ChatStream(msgs, m)
		if err != nil {
			slog.Warn("model failed", "model", m, "error", err)
			_ = failureStore.MarkFailure(m)
			continue
		}
		// Clear failure record on successful request
		_ = failureStore.ClearFailure(m)
		return stream, m, nil
	}
	return nil, "", fmt.Errorf("no free models available")
}

// resolveDisplayNameToFullModel resolves a display name back to the full model name
func resolveDisplayNameToFullModel(displayName string) string {
	for _, fullModel := range freeModels {
		parts := strings.Split(fullModel, "/")
		modelDisplayName := parts[len(parts)-1]
		if modelDisplayName == displayName {
			// Apply model filter if it exists
			if !isModelInFilter(displayName, modelFilter) {
				continue // Skip models not in filter
			}
			return fullModel
		}
	}
	return displayName // fallback to original name if not found
}

// getFreeChatForModel tries to use a specific model first, then falls back to any available free model
func getFreeChatForModel(provider *OpenrouterProvider, msgs []openai.ChatCompletionMessage, requestedModel string) (openai.ChatCompletionResponse, string, error) {
	var resp openai.ChatCompletionResponse

	// First try the requested model if it's in our free models list
	fullModelName := resolveDisplayNameToFullModel(requestedModel)
	if fullModelName != requestedModel || contains(freeModels, fullModelName) {
		skip, err := failureStore.ShouldSkip(fullModelName)
		if err == nil && !skip {
			resp, err = provider.Chat(msgs, fullModelName)
			if err == nil {
				_ = failureStore.ClearFailure(fullModelName)
				return resp, fullModelName, nil
			}
			slog.Warn("requested model failed, trying fallback", "model", fullModelName, "error", err)
			_ = failureStore.MarkFailure(fullModelName)
		}
	}

	// Fallback to any available free model
	return getFreeChat(provider, msgs)
}

// getFreeStreamForModel tries to use a specific model first, then falls back to any available free model
func getFreeStreamForModel(provider *OpenrouterProvider, msgs []openai.ChatCompletionMessage, requestedModel string) (*openai.ChatCompletionStream, string, error) {
	// First try the requested model if it's in our free models list
	fullModelName := resolveDisplayNameToFullModel(requestedModel)
	if fullModelName != requestedModel || contains(freeModels, fullModelName) {
		skip, err := failureStore.ShouldSkip(fullModelName)
		if err == nil && !skip {
			stream, err := provider.ChatStream(msgs, fullModelName)
			if err == nil {
				_ = failureStore.ClearFailure(fullModelName)
				return stream, fullModelName, nil
			}
			slog.Warn("requested model failed, trying fallback", "model", fullModelName, "error", err)
			_ = failureStore.MarkFailure(fullModelName)
		}
	}

	// Fallback to any available free model
	return getFreeStream(provider, msgs)
}

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// isModelInFilter checks if a model name matches any filter pattern (supports partial matches)
func isModelInFilter(modelName string, filter map[string]struct{}) bool {
	if len(filter) == 0 {
		return true // No filter means all models are allowed
	}
	
	for filterPattern := range filter {
		if strings.Contains(modelName, filterPattern) {
			return true
		}
	}
	return false
}
