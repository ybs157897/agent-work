import { describe, expect, it } from 'vitest';
import { currencyTemplate, mergeContentDocuments, ratingTemplate, scoreTemplate, stockTemplate, weatherTemplate } from './content-block-templates';
import { parseContentBlockDocument } from './content-blocks';

describe('LanguageGUI domain templates', () => {
  it('composes currency, weather, stock, score and rating from generic blocks', () => {
    const document = mergeContentDocuments(
      currencyTemplate({ from: { code: 'USD', amount: '$100' }, to: { code: 'EUR', amount: '€90' } }),
      weatherTemplate({ location: '上海', temperature: '28°', condition: '晴', hourly: [{ time: '12:00', temperature: '30°', condition: '晴' }] }),
      stockTemplate({ name: 'Meta', symbol: 'META', price: '$547.10', delta: '+1.12%', labels: ['10:00', '12:00'], values: [544, 547], unit: '$' }),
      scoreTemplate({ league: 'League', status: 'Live', home: { name: 'Team A', score: 3 }, away: { name: 'Team B', score: 1 } }),
      ratingTemplate('这次回答有帮助吗？'),
    );
    expect(document.version).toBe('languagegui/v1');
    expect(document.blocks.map((block) => block.type)).toEqual([
      'metric',
      'metric', 'table',
      'metric', 'chart',
      'metric',
      'rating',
    ]);
    expect(document.blocks.find((block) => block.type === 'rating')).toMatchObject({ question: '这次回答有帮助吗？' });
    const reparsed = parseContentBlockDocument(JSON.stringify(document));
    expect(reparsed?.blocks.find((block) => block.type === 'chart')).toMatchObject({ yDomain: 'auto' });
  });
});
