// Control, Features, Messenger: on or off, its abuse limits and storage.
// Every setting applies after a restart.
'use strict';
(() => {
  Ctl.section({
    group: 'Features', id: 'chat', title: 'Messenger',
    render() {
      const d = Ctl.val('chat.domains').split(',').map(s => s.trim()).filter(Boolean).join(', ');
      return Panel.sec('Messenger', Ctl.sw('chat.on', 'Messenger', `${d ? `Serves ${esc(d)}. ` : ''}Off tells apps the messenger is turned off.`))
        + Panel.sec('Limits', Ctl.num('chat.sendPerHour', 'Sends an hour', 'Per address')
          + Ctl.num('chat.inboxCap', 'Inbox size', 'Messages kept per inbox', 'msgs')
          + Ctl.num('chat.perPairCap', 'From one sender', 'Messages one address can leave in one inbox', 'msgs')
          + Ctl.num('chat.maxMsgBytes', 'Message size', 'Longest message text', 'bytes')
          + Ctl.num('chat.ttlHours', 'Undelivered messages expire after', '', 'h'))
        + Panel.sec('Storage', Ctl.num('chat.accountTTLDays', 'Delete idle addresses after', '0 keeps them, which keeps the counts right', 'days')
          + Ctl.num('chat.maxAccounts', 'Most addresses stored', '0 means no limit')
          + Ctl.num('chat.syncSec', 'Write to disk every', '0 writes each message at once: safest, slower', 's'));
    },
  });
})();
