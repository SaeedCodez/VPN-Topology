// Cloudflare Worker: relays the hub's Telegram Bot API calls (the hub's server
// cannot reach api.telegram.org). Only the methods the hub uses are allowed,
// so it cannot be used as a general proxy. It stores nothing; the bot token
// stays in the hub's env (it is part of the request path, as Telegram requires).
// Deploy: Workers & Pages → Create → Hello World → paste → Deploy, then set
// TELEGRAM_API_ROOT=https://<worker>.<account>.workers.dev in Coolify.
const ALLOWED = /^\/bot\d+:[\w-]+\/(sendMessage|getUpdates|sendChatAction|answerCallbackQuery|editMessageText|editMessageReplyMarkup)$/;

export default {
  async fetch(req) {
    const { pathname } = new URL(req.url);
    if (req.method !== 'POST' || !ALLOWED.test(pathname)) {
      return new Response('not found', { status: 404 });
    }
    return fetch('https://api.telegram.org' + pathname, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: req.body,
    });
  },
};
