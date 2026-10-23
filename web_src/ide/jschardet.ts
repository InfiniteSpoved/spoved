// Stands in for jschardet, which VS Code only uses to guess encodings (`files.autoGuessEncoding`, off here).
export function detect() {
  return null;
}
export function detectAll() {
  return [];
}
export default {detect, detectAll};
