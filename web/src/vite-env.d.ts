/// <reference types="vite/client" />

interface ImportMetaEnv {
  // Версия сборки — git-тег, прокинутый в build (VITE_APP_VERSION). "dev" вне сборки по тегу.
  readonly VITE_APP_VERSION?: string;
}
