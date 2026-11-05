---
description: 同步 upstream/main，处理已有本地修复，然后 push、build 并替换容器网关中的 magpie binary
---
fetch upstream 并尝试 rebase onto upstream/main，注意：如果 baseline 已经修复了 local repo 自行修复的 baseline 问题，则使用 upstream 的修复。对于尚未修复的 baseline 的问题，不需要额外修复。执行完毕后 push, build，然后替换现有容器网关中的 magpie binary

授权进行 subagent 编排，编排策略：/data/projects/instructions/subagent-scheduling-prompt-v4.md
