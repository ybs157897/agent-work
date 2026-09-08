/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_GATEWAY_URL?: string
  readonly VITE_DEV_TOKEN?: string
  readonly VITE_DEFAULT_ROOT?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
