export type Stored = { id: string; text: string }

declare module 'claude-code' {
  interface PluginState {
    'quant-ui': {
      // Which rows of a card are open: one value per drawn row (message or tool call).
      open: StateFamily<Record<string, boolean>>
      // Wake-up batches seen as conversation rows, by the row's id.
      batches: Shaped<Stored[]>
    }
  }
}
