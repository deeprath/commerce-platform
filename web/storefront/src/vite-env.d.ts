/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_MEDIA_BASE_URL?: string;
  readonly VITE_BFF_TARGET?: string;
}
interface ImportMeta {
  readonly env: ImportMetaEnv;
}
