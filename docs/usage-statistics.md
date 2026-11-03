# Usage statistics

[简体中文](./usage-statistics.zh-CN.md)

Settings → Usage shows activity and token consumption over the selected 7/30/90-day period.
The total counts provider-reported input plus output tokens without adding cache or reasoning subsets twice.
Repeated processing of the same content still consumes tokens; this is not a count of unique text.
Calls without reported usage are excluded and identified separately.

The summary compares cumulative processing volume with familiar texts.
Hover, tap, or focus the comparison to see the original token count and a brief explanation.
Reference sources and conversion rules are documented below, rather than shown in the popup.
Daily charts retain numeric token values.
The comparison is approximate: it does not measure generated text length, completed work, or cost.
Changing the interface language changes the reference texts, not the recorded consumption.
Zero remains zero; positive totals below one tenth of a page show “less than 0.1 A4 page”; wholly unreported usage remains unknown.

## Reference sizes

These fixed display estimates use the same conservative text convention as `EstimateTextTokens` in `internal/provider/conversation.go`: ASCII characters divided by three, rounded up, plus two per non-ASCII character.
This convention is not a model tokenizer and does not account for model-specific tokenization or non-text input.
One A4 text page is a display convention of 500 Chinese characters or 500 English words averaging six characters including spaces, giving approximately 1,000 tokens.
Encyclopedia word/character totals use the same convention; illustrations are not included.

The largest reference whose size does not exceed the total is selected, with its multiple rounded to one decimal place.
Chinese interface variants use the Chinese references; other interface languages localize the international titles while retaining the English source editions.

| Chinese reference | Approximate tokens per reference | Fixed source |
| --- | ---: | --- |
| Kong Yiji | 5,200 | [Chinese text in *Nahan*, Project Gutenberg #27166](https://www.gutenberg.org/ebooks/27166) |
| The True Story of Ah Q | 43,000 | [Chinese text in *Nahan*, Project Gutenberg #27166](https://www.gutenberg.org/ebooks/27166) |
| Dream of the Red Chamber | 1,700,000 | [120-chapter Chinese text, Project Gutenberg #24264](https://www.gutenberg.org/ebooks/24264) |
| Encyclopedia of China | 120,000,000 | [Second edition, 32 volumes, approximately 60 million Chinese characters](https://en.chinaculture.org/cn/2010-03/24/content_374662.htm) |

| International reference | Approximate tokens per reference | Fixed source |
| --- | ---: | --- |
| The Gift of the Magi | 3,900 | [English text, Project Gutenberg #7256](https://www.gutenberg.org/ebooks/7256) |
| Alice’s Adventures in Wonderland | 53,000 | [English text, Project Gutenberg #11, Millennium Fulcrum Edition 3.0](https://www.gutenberg.org/ebooks/11) |
| War and Peace | 1,100,000 | [English translation by Louise and Aylmer Maude, Project Gutenberg #2600](https://www.gutenberg.org/ebooks/2600) |
| Encyclopædia Britannica | 88,000,000 | [2010 revised 15th edition, 32 volumes, over 44 million English words](https://www.guinnessworldrecords.com/world-records/67347-largest-encyclopedia-english) |

Gutenberg measurements exclude the service's header/footer and collapse whitespace runs into one ASCII space.
The two *Nahan* stories exclude the trailing “□注釋” annotations and standalone writing-date line; the other downloads retain their edition's headings and notes.
The resulting text estimates before display rounding were 5,238; 42,516; 1,715,389; 3,904; 53,123; and 1,127,678 tokens, respectively.
Only fixed reference sizes and display names are bundled; Vista does not download these texts, run a tokenizer, or request a model when showing the comparison.

Implementation: `web/src/state/tokenScale.ts` and `web/src/StatisticsSettings.vue`.
Accounting and retention: [product behavior](./product.md#activity-and-usage).
