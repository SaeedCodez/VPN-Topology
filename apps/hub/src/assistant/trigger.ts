/**
 * Which Telegram messages the assistant answers. Pure.
 *
 * Only the configured chat, only allowed Telegram user ids, and only when the
 * message is meant for the bot: `/ask …` (or `/ask@bot …`), or a reply to one
 * of the bot's own messages (an alert, a recovery or an earlier answer).
 * Everything else in the group is ignored, so the bot never reads along.
 */

export interface TgMessage {
  message_id: number;
  date: number;
  text?: string;
  caption?: string;
  chat: { id: number | string; type?: string };
  from?: { id: number | string; is_bot?: boolean; first_name?: string; username?: string };
  reply_to_message?: { message_id: number; from?: { id: number | string; is_bot?: boolean } };
}

export type Decision =
  | { answer: true; question: string; replyToBotMessage: number | null; fromId: string; fromName: string }
  | { answer: false; reason: 'other-chat' | 'not-for-bot' | 'not-allowed' | 'empty' | 'from-bot'; fromId?: string; help?: boolean };

export function decide(m: TgMessage, cfg: { chatId: string; botId: string; allowedUserIds: string[] }): Decision {
  if (String(m.chat?.id) !== String(cfg.chatId)) return { answer: false, reason: 'other-chat' };
  if (!m.from || m.from.is_bot) return { answer: false, reason: 'from-bot' };
  const fromId = String(m.from.id);
  const text = (m.text ?? m.caption ?? '').trim();
  const cmd = /^\/ask(?:@\w+)?(?:\s+|$)([\s\S]*)$/i.exec(text);
  const replyToBot = m.reply_to_message && String(m.reply_to_message.from?.id) === String(cfg.botId) ? m.reply_to_message.message_id : null;
  if (!cmd && replyToBot === null) return { answer: false, reason: 'not-for-bot' };
  if (!cfg.allowedUserIds.includes(fromId)) return { answer: false, reason: 'not-allowed', fromId };
  const question = (cmd ? cmd[1] : text).trim();
  if (!question) return { answer: false, reason: 'empty', fromId, help: true };
  return { answer: true, question: question.slice(0, 2000), replyToBotMessage: replyToBot, fromId, fromName: m.from.first_name ?? m.from.username ?? fromId };
}
