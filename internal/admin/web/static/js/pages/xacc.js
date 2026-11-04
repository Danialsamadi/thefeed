// Control, Sources, X accounts: the accounts and the Nitter instances X is
// read through. Every change here applies after a restart.
'use strict';
(() => {
  const S = Panel.S, C = Ctl.C;
  const down = () => Panel.feature?.('x')?.status === 'down';

  Ctl.section({
    group: 'Sources', id: 'xacc', title: 'X accounts',
    summary() {
      const n = plural(C.st.lists.x.items.length, 'account', 'accounts');
      return down() ? [`${n}, paused: no Nitter instance answers`, 'bad'] : n;
    },
    render() {
      const x = C.st.lists.x.items, ins = C.st.lists.instances.items;
      return (down() ? `<div class="notice"><span><b class="sw-down">X is down.</b> No Nitter instance answers, so these accounts are paused.</span><button class="btn quiet" type="button" data-act="c-goto-x" data-k="goto-x">Open X status</button></div>` : '')
        + Panel.sec('Add', Ctl.addForm('x', 'X handle or link', '@handle or x.com/handle', 'Fetched through the Nitter instances below. Applies after a restart.'))
        + Panel.sec(`Accounts, ${x.length}`, Ctl.rows('x', x, 'No X accounts. With none, the server turns X off.'))
        + Panel.sec(`Nitter instances, ${ins.length}`, Ctl.addForm('instances', 'Add an instance', 'nitter.example.com', 'RSS mirrors the server reads X through.') + Ctl.rows('instances', ins, ''),
          'Tried in this order on every check. Changes apply after a restart.');
    },
  });

  Panel.acts['c-goto-x'] = () => {
    S.view = 'board';
    Panel.acts.open({ dataset: { id: 'x' } });
  };
})();
