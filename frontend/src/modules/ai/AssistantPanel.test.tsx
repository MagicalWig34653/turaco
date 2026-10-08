import { renderToStaticMarkup } from 'react-dom/server';
import { expect, it } from 'vitest';
import { InertAnswer } from './AssistantPanel';
it('renders hostile model HTML, Markdown links, images and URLs as inert text', () => {
  const html = renderToStaticMarkup(
    <InertAnswer
      text={
        '<img src="https://evil.example/track"> <script>alert(1)</script> ![image](https://evil.example/image) [link](javascript:alert(1)) https://evil.example'
      }
    />,
  );
  expect(html).not.toMatch(/<(img|script|a|iframe)\b/);
  expect(html).toContain('&lt;img');
  expect(html).toContain('![image](https://evil.example/image)');
  expect(html).toContain('[link](javascript:alert(1))');
});
