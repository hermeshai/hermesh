# Models

her never assumes a model. It uses the `model` you configured; otherwise the first provider that has what it needs; otherwise it says nothing is set up and exits 2.

```bash
her login
```

To pick the default yourself: `her config set model provider/model-id`. For one run: `-m provider/model-id`.

## Ways to connect

| Way | Command | Notes |
|---|---|---|
| API key | `her login deepseek` | The key is checked with a real request and kept in the keychain (macOS) or a 0600 file. A key in the provider's environment variable works too |
| Token Plan | `her login bailian-token-plan` | Bailian, Tencent, Volcengine, Zhipu, Z.ai |
| ChatGPT subscription | `her login chatgpt` | Signs in with your browser. `her logout chatgpt` signs out; `--forget` also forgets the registration |
| Local | `her login ollama` | No key. LM Studio and llama.cpp go as your own service |
| Your own service | `her login work --base-url URL` | A company gateway, Azure, a self-hosted model. `--api` says what it speaks (default `openai-chat`), `--no-key` that it takes no key. Written to `~/.her/models.toml` |

`her models` lists the providers, what each needs and what you can use now. `hide` and `unhide` change the menu. `--refresh` asks the providers again.

## Is this model working?

```bash
her models check provider/model-id
```

This makes real requests and costs a little: text, tools, parallel tools, thinking levels, images, cache, cancelling. `her doctor --online` makes one request to the default model.

## Tested

| Provider | State |
|---|---|
| Bailian Token Plan | Tested with `her models check` and real tasks |
| Exa, Parallel (web search) | Tested with real searches |
| DeepSeek, OpenAI, Anthropic, Google Gemini, OpenRouter, Qwen, Kimi, Zhipu GLM, other Token Plans | Built in; not yet tested against the real service. `her models check` tells you in a minute |
