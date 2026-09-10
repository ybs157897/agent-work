/** Browser regression check, served by Vite at /scripts/assert-chat-output-layout.js. */
export function assertChatOutputLayout(prose, expectedBlocks) {
  if (!prose) throw new Error('Missing Agent reply');
  const types = [...prose.querySelectorAll('[data-content-block]')]
    .map((element) => element.getAttribute('data-content-block'));
  if (expectedBlocks && types.join(',') !== expectedBlocks.join(',')) {
    throw new Error(`Unexpected content blocks: ${types.join(',')}`);
  }

  // Exercise the real stylesheet and browser cascade, including heading and
  // nested quote spacing; static React markup cannot catch margin overrides.
  const fixture = document.createElement('div');
  fixture.className = 'chat-prose';
  fixture.innerHTML = '<p>First</p><p>Second</p><h2>Heading</h2><blockquote><p>Quote</p><p>Next</p></blockquote>';
  prose.parentElement.append(fixture);
  try {
    const margin = (selector) => parseFloat(getComputedStyle(fixture.querySelector(selector)).marginTop);
    const first = margin(':scope > p');
    const paragraph = margin(':scope > p + p');
    const heading = margin('h2');
    const quote = margin('blockquote p + p');
    if (first !== 0 || paragraph <= 0 || heading <= paragraph || quote <= 0) {
      throw new Error(`Markdown spacing regressed: ${JSON.stringify({ first, paragraph, heading, quote })}`);
    }
    return { types, margins: { first, paragraph, heading, quote } };
  } finally {
    fixture.remove();
  }
}
