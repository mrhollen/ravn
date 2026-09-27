// alpinejs ships no TypeScript types. Minimal ambient declaration: we only use
// Alpine.data / Alpine.start from the default export at runtime.
declare module "alpinejs" {
  interface AlpineApi {
    data(name: string, definition: unknown): void;
    start(): Promise<void>;
    [key: string]: unknown;
  }
  const Alpine: AlpineApi & Record<string, unknown>;
  export default Alpine;
}
