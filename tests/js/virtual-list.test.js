import test from 'node:test';
import assert from 'node:assert/strict';
import { VirtualList } from '../../js/virtual-list.js';

// A tiny DOM: just enough for VirtualList (elements with style, dataset, append and remove).
function el() {
  const node = {
    children: [],
    style: {},
    dataset: {},
    listeners: {},
    scrollTop: 0,
    clientHeight: 100,
    append(child) {
      child.parent = node;
      node.children.push(child);
    },
    remove() {
      node.parent.children.splice(node.parent.children.indexOf(node), 1);
    },
    addEventListener(type, fn) {
      node.listeners[type] = fn;
    },
  };
  return node;
}

function setup(t) {
  const saved = { document: globalThis.document, raf: globalThis.requestAnimationFrame, RO: globalThis.ResizeObserver };
  let frame = null;
  let observed = null;
  globalThis.document = { createElement: () => el() };
  globalThis.requestAnimationFrame = (fn) => ((frame = fn), 1);
  globalThis.ResizeObserver = class {
    constructor(fn) {
      this.fn = fn;
    }
    observe() {
      observed = this.fn;
    }
  };
  t.after(() => Object.assign(globalThis, { document: saved.document, requestAnimationFrame: saved.raf, ResizeObserver: saved.RO }));
  return {
    flush: () => {
      const f = frame;
      frame = null;
      f?.();
    },
    resize: () => observed(),
  };
}

const visible = (list) => list.inner.children.map((c) => Number(c.dataset.idx)).sort((a, b) => a - b);

test('only the rows near the viewport exist', (t) => {
  const { flush, resize } = setup(t);
  const sc = el();
  let rendered = 0;
  const list = new VirtualList(sc, 20, (item) => (rendered++, Object.assign(el(), { item })));
  const items = Array.from({ length: 1000 }, (_, i) => i);
  list.setItems(items);
  assert.equal(list.inner.style.height, '20000px');
  assert.deepEqual(visible(list), Array.from({ length: 12 }, (_, i) => i)); // 5 visible + 6 overscan + the partial
  assert.equal(list.inner.children[3].style.top, '60px');
  assert.equal(list.inner.children[3].style.height, '20px');

  sc.scrollTop = 4000;
  sc.listeners.scroll();
  sc.listeners.scroll(); // coalesced into one frame
  flush();
  assert.deepEqual(visible(list), Array.from({ length: 18 }, (_, i) => 194 + i));

  const before = rendered;
  list.refresh();
  assert.equal(rendered - before, 18, 'refresh re-renders the visible rows');

  list.scrollToIndex(500);
  assert.equal(sc.scrollTop, 500 * 20 - 50 + 10);
  assert.ok(visible(list).includes(500));

  list.setItems(items.slice(0, 3), { keepScroll: true });
  assert.equal(sc.scrollTop, 9960, 'keepScroll leaves the position alone');
  list.setItems(items.slice(0, 3));
  assert.equal(sc.scrollTop, 0);
  assert.deepEqual(visible(list), [0, 1, 2]);

  sc.clientHeight = 0; // not laid out yet: assume a screenful
  resize();
  flush();
  assert.deepEqual(visible(list), [0, 1, 2]);
  assert.equal(list.scrollToIndex(-5), undefined);
  assert.equal(sc.scrollTop, 0);
});

test('works without ResizeObserver', (t) => {
  setup(t);
  delete globalThis.ResizeObserver;
  const list = new VirtualList(el(), 10, () => el());
  list.setItems([1, 2]);
  assert.deepEqual(visible(list), [0, 1]);
});
