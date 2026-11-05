// Cloudflare Worker: relays the hub's Telegram Bot API calls (the hub's server
// cannot reach api.telegram.org). Only the methods the hub uses are allowed,
// so it cannot be used as a general proxy (sendPhoto/sendDocument carry the
// pictures of the board). It stores nothing; the bot token stays in the hub's
// env (it is part of the request path, as Telegram requires).
// Deploy: Workers & Pages → Create → Hello World → paste → Deploy, then set
// TELEGRAM_API_ROOT=https://<worker>.<account>.workers.dev in Coolify.
// Optional: set the Worker variable BOT_ID to your bot's numeric id (the part
// of the token before ":"; not a secret) so the relay serves only that bot.
// answerCallbackQuery / editMessageText / editMessageReplyMarkup are the ops
// approval buttons.
const ALLOWED = /^\/bot(\d+):[\w-]+\/(sendMessage|sendPhoto|sendDocument|getUpdates|sendChatAction|answerCallbackQuery|editMessageText|editMessageReplyMarkup)$/;

export default {
  async fetch(req, env) {
    const { pathname } = new URL(req.url);
    const m = ALLOWED.exec(pathname);
    if (req.method !== 'POST' || !m || (env && env.BOT_ID && m[1] !== String(env.BOT_ID))) {
      return new Response('not found', { status: 404 });
    }
    return fetch('https://api.telegram.org' + pathname, {
      method: 'POST',
      // JSON for most methods, multipart/form-data (with its boundary) for sendPhoto/sendDocument.
      headers: { 'content-type': req.headers.get('content-type') || 'application/json' },
      body: req.body,
    });
  },
};
