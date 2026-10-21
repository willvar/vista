import { createServer } from 'node:net'

// Give each isolated smoke instance its own explicit port, separate from the user's Vista.
export async function availableVistaPort() {
  const listener = createServer()
  await new Promise((resolve, reject) => {
    listener.once('error', reject)
    listener.listen(0, '127.0.0.1', resolve)
  })
  const port = String(listener.address().port)
  await new Promise((resolve, reject) => listener.close(error => error ? reject(error) : resolve()))
  return port
}
