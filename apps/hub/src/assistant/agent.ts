import { ChatFn, ChatMessage } from './openrouter';
import { IMAGE_TOOL, runTool, ToolContext, TOOLS } from './tools';

/**
 * One question → one answer. The model may call the read-only tools for up to
 * MAX_ROUNDS rounds; the last round offers no tools so it has to answer.
 * No I/O of its own: the chat function and the tools are passed in.
 */

export const MAX_ROUNDS = 6;

export interface AgentInput {
  question: string;
  /** Earlier turns of this thread (a reply to a previous answer). */
  thread?: { role: 'user' | 'assistant'; content: string }[];
  /** The bot message the user replied to (an alert or recovery), if any. */
  replyTo?: { kind: string; alertKey?: string; at: number; text: string };
  now: number;
  boardUrl: string;
}

export interface AgentResult {
  text: string;
  rounds: number;
  toolCalls: { name: string; args: string }[];
  usage: { promptTokens: number; completionTokens: number; costUsd: number };
}

export function systemPrompt(now: number, boardUrl: string, images = false): string {
  const lines = [
    'You are the operations assistant of a production VPN service with paying users. You answer questions in a Telegram group from the owner and the people they allowed.',
    'You see the system only through the live map ("VPN Topology"): nodes (servers, relays, exits), links between them, routes from users to exits, checks, alerts and an event log, live and in the past. Use the tools to look things up; never invent facts, numbers, ids or times. If the tools do not show it, say you do not know.',
    'You are strictly read-only. You cannot change, restart or reconfigure anything and must not claim you did. When a fix is needed, explain it and quote only commands from the runbook the tools return; the owner runs them.',
    'Terms: a route is broken when a hop on it is down; the root cause is the first failing hop of a broken route. "users" are live counts from the panel or estimates. Times in the data are UTC; tell the user times in Tehran time.',
    'Answer in Persian (Farsi), short and concrete: what is happening, since when, how many users are affected, the likely cause, and the next step. Use names of nodes/links as the map calls them. Plain text only: no Markdown tables, no headings; short lines and "-" lists are fine.',
  ];
  if (images) {
    lines.push('When the user asks for a picture, image, screenshot or "عکس/تصویر" of the board or of part of it, call board_image (whole board, a route, a path from → via → to, or chosen nodes/links). It sends the picture itself; then answer in one short line.');
  }
  lines.push(`Now: ${new Date(now).toISOString()} (UTC). Board: ${boardUrl}`);
  return lines.join('\n');
}

export function userMessage(inp: AgentInput): string {
  if (!inp.replyTo) return inp.question;
  const what = inp.replyTo.kind === 'resolved' ? 'a recovery notice' : inp.replyTo.kind === 'alert' ? 'an alert' : inp.replyTo.kind === 'image' ? 'a picture of the board' : 'a message';
  return [
    `[The user is replying to ${what} the bot sent at ${new Date(inp.replyTo.at).toISOString()}${inp.replyTo.alertKey ? ` (alert "${inp.replyTo.alertKey}")` : ''}. Tools default to that moment; pass at to look at another time.]`,
    `[That message: ${stripHtml(inp.replyTo.text)}]`,
    '',
    inp.question,
  ].join('\n');
}

export async function answer(inp: AgentInput, chat: ChatFn, tools: ToolContext): Promise<AgentResult> {
  const offered = tools.image ? [...TOOLS, IMAGE_TOOL] : TOOLS;
  const messages: ChatMessage[] = [{ role: 'system', content: systemPrompt(inp.now, inp.boardUrl, !!tools.image) }, ...(inp.thread ?? []).slice(-12), { role: 'user', content: userMessage(inp) }];
  const usage = { promptTokens: 0, completionTokens: 0, costUsd: 0 };
  const calls: { name: string; args: string }[] = [];
  for (let round = 1; round <= MAX_ROUNDS; round++) {
    const last = round === MAX_ROUNDS;
    const r = await chat(messages, last ? [] : offered);
    usage.promptTokens += r.usage.promptTokens;
    usage.completionTokens += r.usage.completionTokens;
    usage.costUsd += r.usage.costUsd ?? 0;
    if (!r.toolCalls.length || last) {
      return { text: (r.content ?? '').trim() || 'جوابی از مدل نگرفتم.', rounds: round, toolCalls: calls, usage };
    }
    messages.push({ role: 'assistant', content: r.content, tool_calls: r.toolCalls });
    for (const tc of r.toolCalls) {
      calls.push({ name: tc.function.name, args: tc.function.arguments });
      messages.push({ role: 'tool', tool_call_id: tc.id, content: await runTool(tc.function.name, tc.function.arguments, tools) });
    }
  }
  /* unreachable: the last round always returns */
  return { text: '', rounds: MAX_ROUNDS, toolCalls: calls, usage };
}

export function stripHtml(s: string): string {
  return s.replace(/<[^>]+>/g, '').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
}

/** Telegram's limit is 4096 characters per message; split on line breaks. */
export function splitMessage(text: string, max = 4000): string[] {
  const out: string[] = [];
  let cur = '';
  for (const line of text.split('\n')) {
    const piece = line.length > max ? line.match(new RegExp(`[\\s\\S]{1,${max}}`, 'g'))! : [line];
    for (const p of piece) {
      if (cur && cur.length + 1 + p.length > max) {
        out.push(cur);
        cur = p;
      } else cur = cur ? `${cur}\n${p}` : p;
    }
  }
  if (cur) out.push(cur);
  return out.length ? out : [''];
}
