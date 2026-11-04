// Control, Features, Telegram over DNS: on or off, and its tunnel limits.
// Every setting applies after a restart.
'use strict';
(() => {
  Ctl.section({
    group: 'Features', id: 'mt', title: 'Telegram over DNS',
    render() {
      const d = Ctl.val('mt.domains').split(',').map(s => s.trim()).filter(Boolean).join(', ');
      return Panel.sec('Telegram over DNS', Ctl.sw('mt.on', 'Telegram over DNS', `${d ? `Serves ${esc(d)}. ` : ''}Off tells apps the tunnel is turned off.`))
        + Panel.sec('Limits', Ctl.num('mt.maxTunnels', 'Open tunnels at once', "0 means no limit; the server's open-file limit is then the ceiling")
          + Ctl.num('mt.idleMin', 'Close an idle tunnel after', 'Apps reopen it on their next use', 'min')
          + Ctl.num('mt.bufferKB', 'Buffer per tunnel', 'Memory each open tunnel uses', 'KB')
          + Ctl.num('mt.downKBMin', 'Download speed per tunnel', '0 means no limit', 'KB/min')
          + Ctl.num('mt.dialSec', 'Give up on a data centre after', '', 's'));
    },
  });
})();
