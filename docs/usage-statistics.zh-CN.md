# 使用情况统计

[English](./usage-statistics.md)

设置 → 使用情况展示所选 7/30/90 天周期内的活跃情况和词元消耗。
总量按服务商上报的输入加输出词元统计，不重复加算缓存和推理子项。
重复处理同一内容仍会消耗词元，因此这不是不重复文字的字数。
未上报用量的调用单独注明，不计入总量。

总量卡片用熟悉的文本类比累计处理量。
悬停、点击或通过键盘聚焦，可查看原始词元数和简短说明。
参照来源和换算规则记录在下文，不在弹层中展示。
每日图表仍使用词元数值。
类比仅帮助理解规模，不代表生成的文字长度、完成的任务或费用。
切换界面语言只改变参照物，不改变已统计的消耗。
零用量仍为零；正数不足十分之一页时显示「不到 0.1 页 A4 纸」；全部未上报时仍显示未知。

## 参照规模

固定展示基准沿用 `internal/provider/conversation.go` 中 `EstimateTextTokens` 的保守文本估算：ASCII 字符数除以三向上取整，加上非 ASCII 字符数的两倍。
它不是模型分词器，不反映具体模型的分词方式或非文本输入。
一页 A4 文字按 500 个中文字，或 500 个英文单词、平均每词含空格六个字符约定，均约合 1,000 词元。
百科全书的字数、词数使用相同约定，插图不计入。

总量达到某个参照规模后，切换到其中最大的参照物，倍数保留一位小数。
中文各变体使用中文参照物；其他语言翻译国际作品名称，换算仍以英文来源版本为准。

| 中文参照物 | 每份参照约合词元 | 固定来源 |
| --- | ---: | --- |
| 《孔乙己》 | 5,200 | [《呐喊》所收中文文本，Project Gutenberg #27166](https://www.gutenberg.org/ebooks/27166) |
| 《阿Q正传》 | 43,000 | [《呐喊》所收中文文本，Project Gutenberg #27166](https://www.gutenberg.org/ebooks/27166) |
| 《红楼梦》 | 1,700,000 | [中文 120 回本，Project Gutenberg #24264](https://www.gutenberg.org/ebooks/24264) |
| 《中国大百科全书》 | 120,000,000 | [第二版，32 卷，约 6,000 万字](https://en.chinaculture.org/cn/2010-03/24/content_374662.htm) |

| 国际参照物 | 每份参照约合词元 | 固定来源 |
| --- | ---: | --- |
| 《麦琪的礼物》 | 3,900 | [英文文本，Project Gutenberg #7256](https://www.gutenberg.org/ebooks/7256) |
| 《爱丽丝梦游仙境》 | 53,000 | [英文文本，Project Gutenberg #11，Millennium Fulcrum Edition 3.0](https://www.gutenberg.org/ebooks/11) |
| 《战争与和平》 | 1,100,000 | [Louise 和 Aylmer Maude 英译本，Project Gutenberg #2600](https://www.gutenberg.org/ebooks/2600) |
| 《大英百科全书》 | 88,000,000 | [2010 年修订第 15 版，32 卷，逾 4,400 万英文单词](https://www.guinnessworldrecords.com/world-records/67347-largest-encyclopedia-english) |

Gutenberg 文本计量排除服务附加的头尾信息，并将连续空白折叠为一个 ASCII 空格。
《呐喊》两篇作品去除末尾「□注釋」注释和独立写作日期行；其他下载文本保留所用版本的标题和注释。
六篇作品在展示取整前依次估算为 5,238、42,516、1,715,389、3,904、53,123、1,127,678 词元。
只内置固定规模与展示名称，展示时不下载作品、不运行分词器，也不请求模型。

实现入口：`web/src/state/tokenScale.ts`、`web/src/StatisticsSettings.vue`。
统计口径与保留周期：[产品行为](./product.zh-CN.md#活跃与用量)。
