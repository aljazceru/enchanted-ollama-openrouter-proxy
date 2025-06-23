# Enchanted Proxy for OpenRouter
This repository is specifically made for use with the [Enchanted project](https://github.com/gluonfield/enchanted/tree/main).
The original author of this proxy is [marknefedov](https://github.com/marknefedov/ollama-openrouter-proxy).

## Description
This repository provides a proxy server that emulates [Ollama's REST API](https://github.com/ollama/ollama) but forwards requests to [OpenRouter](https://openrouter.ai/). It uses the [sashabaranov/go-openai](https://github.com/sashabaranov/go-openai) library under the hood, with minimal code changes to keep the Ollama API calls the same. This allows you to use Ollama-compatible tooling and clients, but run your requests on OpenRouter-managed models.
Currently, it is enough for usage with [Jetbrains AI assistant](https://blog.jetbrains.com/ai/2024/11/jetbrains-ai-assistant-2024-3/#more-control-over-your-chat-experience-choose-between-gemini,-openai,-and-local-models). 

## Features
- **Free Mode (Default)**: Automatically selects and uses free models from OpenRouter with intelligent fallback. Enabled by default unless `FREE_MODE=false` is set.
- **Model Filtering**: You can provide a `models-filter` file in the same directory as the proxy. Each line in this file should contain a single model name. The proxy will only show models that match these entries. If the file doesn’t exist or is empty, no filtering is applied.
  
  **Note**: OpenRouter model names may sometimes include a vendor prefix, for example `deepseek/deepseek-chat-v3-0324:free`. To make sure filtering works correctly, remove the vendor part when adding the name to your `models-filter` file, e.g. `deepseek-chat-v3-0324:free`.
  
- **Ollama-like API**: The server listens on `11434` and exposes endpoints similar to Ollama (e.g., `/api/chat`, `/api/tags`).
- **Model Listing**: Fetch a list of available models from OpenRouter.
- **Model Details**: Retrieve metadata about a specific model.
- **Streaming Chat**: Forward streaming responses from OpenRouter in a chunked JSON format that is compatible with Ollama’s expectations.

## Usage
You can provide your **OpenRouter** (OpenAI-compatible) API key through an environment variable:

### Environment Variable

    export OPENAI_API_KEY="your-openrouter-api-key"
    ./ollama-proxy

### Free Mode (Default Behavior)

The proxy operates in **free mode** by default, automatically selecting from available free models on OpenRouter. This provides cost-effective usage without requiring manual model selection.

    # Free mode is enabled by default - no configuration needed
    export OPENAI_API_KEY="your-openrouter-api-key"
    ./ollama-proxy

    # To disable free mode and use all available models
    export FREE_MODE=false
    export OPENAI_API_KEY="your-openrouter-api-key"
    ./ollama-proxy

#### How Free Mode Works

- **Automatic Model Discovery**: Fetches and caches available free models from OpenRouter
- **Intelligent Fallback**: If a requested model fails, automatically tries other available free models
- **Failure Tracking**: Temporarily skips models that have recently failed (15-minute cooldown)
- **Model Prioritization**: Tries models in order of context length (largest first)
- **Cache Management**: Maintains a `free-models` file for quick startup and a `failures.db` SQLite database for failure tracking

Once running, the proxy listens on port `11434`. You can make requests to `http://localhost:11434` with your Ollama-compatible tooling.

## API Endpoints

The proxy provides both Ollama-compatible and OpenAI-compatible endpoints:

### Ollama API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/` | Health check - returns "Ollama is running" |
| `HEAD` | `/` | Health check (head request) |
| `GET` | `/api/tags` | List available models in Ollama format |
| `POST` | `/api/show` | Get model details |
| `POST` | `/api/chat` | Chat completion with streaming support |

#### Example Requests

**List Models:**
```bash
curl http://localhost:11434/api/tags
```

**Chat Completion:**
```bash
curl -X POST http://localhost:11434/api/chat \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-chat-v3-0324:free",
    "messages": [
      {"role": "user", "content": "Hello!"}
    ],
    "stream": true
  }'
```

**Model Details:**
```bash
curl -X POST http://localhost:11434/api/show \
  -H "Content-Type: application/json" \
  -d '{"name": "deepseek-chat-v3-0324:free"}'
```

### OpenAI API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/v1/models` | List available models in OpenAI format |
| `POST` | `/v1/chat/completions` | Chat completion with streaming support |

#### Example Requests

**List Models (OpenAI format):**
```bash
curl http://localhost:11434/v1/models
```

**Chat Completion (OpenAI format):**
```bash
curl -X POST http://localhost:11434/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-chat-v3-0324:free",
    "messages": [
      {"role": "user", "content": "Hello!"}
    ],
    "stream": false
  }'
```

## Installation
1. **Clone the Repository**:

       git clone https://github.com/your-username/ollama-openrouter-proxy.git
       cd ollama-openrouter-proxy

2. **Install Dependencies**:

       go mod tidy

3. **Build**:

       go build -o ollama-proxy

## Docker Usage

### Using Docker Compose (Recommended)

1. **Clone the repository and create environment file**:
   ```bash
   git clone https://github.com/your-username/ollama-openrouter-proxy.git
   cd ollama-openrouter-proxy
   cp .env.example .env
   ```

2. **Edit `.env` file with your OpenRouter API key**:
   ```bash
   OPENAI_API_KEY=your-openrouter-api-key
   FREE_MODE=true
   ```

3. **Run with Docker Compose**:
   ```bash
   docker-compose up -d
   ```

The service will be available at `http://localhost:11434`.

### Using Docker directly

```bash
docker build -t ollama-proxy .
docker run -p 11434:11434 -e OPENAI_API_KEY="your-openrouter-api-key" ollama-proxy
```
