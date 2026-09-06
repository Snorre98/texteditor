/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_ENGINE_URL?: string;
  readonly VITE_ENGINE_PORT?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}

declare module "*.vue" {
  import type { DefineComponent } from "vue";
  const component: DefineComponent<Record<string, unknown>, Record<string, unknown>, unknown>;
  export default component;
}
