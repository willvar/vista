// Minimal AWS event-stream frames for local Bedrock ConverseStream checks.
function crc32(bytes) {
  let crc = 0xffffffff
  for (const byte of bytes) {
    crc ^= byte
    for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ (crc & 1 ? 0xedb88320 : 0)
  }
  return (crc ^ 0xffffffff) >>> 0
}

function stringHeader(name, value) {
  const key = Buffer.from(name)
  const text = Buffer.from(value)
  const header = Buffer.alloc(1 + key.length + 1 + 2 + text.length)
  header.writeUInt8(key.length, 0)
  key.copy(header, 1)
  header.writeUInt8(7, 1 + key.length)
  header.writeUInt16BE(text.length, 2 + key.length)
  text.copy(header, 4 + key.length)
  return header
}

function frame(type, payload) {
  const headers = Buffer.concat([
    stringHeader(':event-type', type),
    stringHeader(':message-type', 'event'),
    stringHeader(':content-type', 'application/json'),
  ])
  const body = Buffer.from(JSON.stringify(payload))
  const prelude = Buffer.alloc(12)
  prelude.writeUInt32BE(12 + headers.length + body.length + 4, 0)
  prelude.writeUInt32BE(headers.length, 4)
  prelude.writeUInt32BE(crc32(prelude.subarray(0, 8)), 8)
  const message = Buffer.concat([prelude, headers, body])
  const checksum = Buffer.alloc(4)
  checksum.writeUInt32BE(crc32(message), 0)
  return Buffer.concat([message, checksum])
}

export function bedrockReply(text) {
  return Buffer.concat([
    frame('contentBlockDelta', { contentBlockIndex: 0, delta: { text } }),
    frame('messageStop', { stopReason: 'end_turn' }),
  ])
}
