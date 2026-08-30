# Use the official Gemini Go SDK directly

Status: accepted

The analytics service will call Gemini through Google's official `google.golang.org/genai` SDK instead of introducing a provider abstraction or a gateway. The first product supports Gemini only, so a generic provider interface would add indirection without a second real adapter. The model is configurable from Home Assistant and defaults to a Flash model; the API key remains in the external service configuration.

## Considered Options

- Call Gemini through the official Go SDK.
- Add a multi-provider library such as `go-llm`.
- Add LiteLLM or another gateway.

## Consequences

The service owns a small Gemini adapter and validates structured output against its report model. A future provider can be added behind a new seam if it becomes a real requirement; the first version does not pay for that hypothetical variation.
