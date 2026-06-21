# LLM Inspector

LLM Inspector is a high-performance HTTP reverse proxy and real-time dashboard designed for OpenAI-compatible LLM serving endpoints. It transparently proxies chat completion requests to your actual upstream LLM server, intercepts the Server-Sent Events (SSE) stream, and calculates real-time performance metrics without adding latency to the client.

## Features

- **Transparent Proxy**: Fully compatible with the OpenAI API format. Just point your clients to the inspector instead of your upstream server.
- **Asynchronous Processing**: Stream chunk parsing and metric calculations are performed entirely asynchronously using a Golang EventBus, ensuring zero blocking on the actual proxy flow.
- **Real-Time Token Counting**: Integrates `tiktoken-go` to precisely count tokens as they stream.
- **Reasoning Token Support**: Capable of separating and counting `reasoning_content` (used by models like DeepSeek R1) vs standard `content`.
- **Advanced Metrics**: Accurately calculates **TTFT (Time To First Token)** and **E2E (End to End)** latency for every request.
- **SQLite Persistence**: Completed request statistics are safely stored in an SQLite database.
- **Live Dashboard**: A beautiful, dark-mode React frontend that connects via WebSocket to display real-time streaming status and aggregated user metrics.

## Architecture

1. **Golang Backend (`/backend`)**: Runs the reverse proxy on one port (e.g., `8080`) and a WebSocket API server on another port (e.g., `8081`).
2. **React Frontend (`/frontend`)**: A Vite-powered React dashboard utilizing a premium glassmorphism Vanilla CSS design system.

---

## Getting Started

### Prerequisites

- [Go](https://go.dev/) 1.20 or later
- [Node.js](https://nodejs.org/) 18 or later

### 1. Build and Run the Backend

Navigate to the `backend` directory, install dependencies, and build the project:

```bash
cd backend
go mod tidy
go build -o llminspector
```

Run the backend proxy:

```bash
./llminspector --target="https://api.openai.com" --proxy-port="8080" --api-port="8081" --user-header="X-User-ID"
```

#### Backend Command-Line Arguments:

| Argument | Default | Description |
| :--- | :--- | :--- |
| `--target` | `https://api.openai.com` | The upstream OpenAI-compatible server address to proxy requests to. |
| `--proxy-port` | `8080` | The port the reverse proxy will listen on. |
| `--api-port` | `8081` | The port for the WebSocket/API dashboard server. |
| `--user-header` | `X-User-ID` | The HTTP header used to differentiate requests from different users. |
| `--db` | `metrics.db` | Path to the SQLite database file for saving completed metrics. |
| `--tiktoken` | `true` | Set to `true` to use `tiktoken` for precise counting, or `false` for faster length-based estimation. |

### 2. Run the Frontend Dashboard

Open a new terminal window, navigate to the `frontend` directory, and start the Vite dev server:

```bash
cd frontend
npm install
npm run dev
```

Open your browser to `http://localhost:5173`. In the connection bar, enter the backend's API WebSocket URL (e.g., `ws://localhost:8081/ws`) and click **Connect**.

---

## Example Usage

Once both the backend and frontend are running, you can send requests to your local proxy port (`8080`) just as you would to your upstream provider.

To easily differentiate users in the dashboard, be sure to pass the header defined in `--user-header` (default: `X-User-ID`).

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "X-User-ID: alice-dev" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Explain asynchronous programming in Go."}],
    "stream": true
  }'
```

As the response streams back to your curl client, you will instantly see the request appear in the **"Active Streams"** section of the dashboard, showing the real-time TTFT and ticking token counts. Once it finishes, it will move to **"Recent Completed"** and save to the SQLite database.
