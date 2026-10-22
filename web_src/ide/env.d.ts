/// <reference types="vite/client" />
import type {IMonacoEnvironment} from '@codingame/monaco-vscode-api/vscode/vs/base/browser/browser';

declare global {
  interface Window {
    MonacoEnvironment: IMonacoEnvironment;
  }
}
