import { AssistantModel } from '../contract';

/**
 * Minimal OpenRouter client (OpenAI-compatible chat completions with tool
 * calling). The key is passed per call: it comes from the encrypted settings
 * and is never logged; error texts are cleaned of it.
 */

/** Overridable only for local end-to-end tests against a fake server. */
export const OPENROUTER = (process.env.OPENROUTER_API_ROOT || 'https://openrouter.ai/api/v1').replace(/\/+$/, '');

export type ChatMessage =
  | { role: 'system' | 'user'; content: string }
  | { role: 'assistant'; content: string | null; tool_calls?: ToolCall[] }
  | { role: 'tool'; tool_call_id: string; content: string };

export interface ToolCall {
  id: string;
  type: 'function';
  function: { name: string; arguments: string };
}

export interface ToolDef {
  type: 'function';
  function: { name: string; description: string; parameters: Record<string, unknown> };
}

export interface ChatResult {
  content: string | null;
  toolCalls: ToolCall[];
  usage: { promptTokens: number; completionTokens: number; costUsd: number | null };
}

export type ChatFn = (messages: ChatMessage[], tools: ToolDef[]) => Promise<ChatResult>;

export class OpenRouterError extends Error {}

export function openRouterChat(opts: { key: string; model: string; publicUrl: string; fetchFn?: typeof fetch; timeoutMs?: number; maxTokens?: number }): ChatFn {
  const f = opts.fetchFn ?? fetch;
  return async (messages, tools) => {
    let res: Response;
    try {
      res = await f(`${OPENROUTER}/chat/completions`, {
        method: 'POST',
        headers: {
          authorization: `Bearer ${opts.key}`,
          'content-type': 'application/json',
          // OpenRouter attribution headers (optional): shows the app in the OpenRouter dashboard.
          'http-referer': opts.publicUrl,
          'x-title': 'VPN Topology',
        },
        body: JSON.stringify({
          model: opts.model,
          messages,
          ...(tools.length ? { tools, tool_choice: 'auto' } : {}),
          max_tokens: opts.maxTokens ?? 1500,
          temperature: 0.2,
          usage: { include: true },
        }),
        signal: AbortSignal.timeout(opts.timeoutMs ?? 60_000),
      });
    } catch (e: any) {
      throw new OpenRouterError(clean(`network: ${e?.cause?.code ?? e?.name ?? 'error'} ${e?.cause?.message ?? e?.message ?? ''}`, opts.key));
    }
    const body: any = await res.json().catch(() => ({}));
    if (!res.ok || body.error) {
      const msg = body?.error?.message ?? res.statusText;
      throw new OpenRouterError(clean(`${res.status} ${msg}`, opts.key));
    }
    const m = body.choices?.[0]?.message ?? {};
    return {
      content: typeof m.content === 'string' ? m.content : null,
      toolCalls: Array.isArray(m.tool_calls) ? m.tool_calls : [],
      usage: {
        promptTokens: body.usage?.prompt_tokens ?? 0,
        completionTokens: body.usage?.completion_tokens ?? 0,
        costUsd: typeof body.usage?.cost === 'number' ? body.usage.cost : null,
      },
    };
  };
}

/** Public model list (no key needed), trimmed for the board's model picker. */
export async function listModels(fetchFn: typeof fetch = fetch): Promise<AssistantModel[]> {
  const res = await fetchFn(`${OPENROUTER}/models`, { signal: AbortSignal.timeout(20_000) });
  if (!res.ok) throw new OpenRouterError(`${res.status} ${res.statusText}`);
  const body: any = await res.json();
  const perM = (v: unknown) => (v == null || Number.isNaN(Number(v)) ? null : Math.round(Number(v) * 1e6 * 1000) / 1000);
  return (body.data ?? [])
    .map((m: any) => ({
      id: String(m.id),
      name: String(m.name ?? m.id),
      contextLength: m.context_length ?? null,
      promptPrice: perM(m.pricing?.prompt),
      completionPrice: perM(m.pricing?.completion),
      tools: Array.isArray(m.supported_parameters) && m.supported_parameters.includes('tools'),
    }))
    .sort((a: AssistantModel, b: AssistantModel) => a.id.localeCompare(b.id));
}

function clean(s: string, key: string): string {
  return key ? s.split(key).join('<key>') : s;
}
