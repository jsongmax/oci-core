<script setup lang="ts">
import { computed, defineAsyncComponent } from 'vue'
import { useRoute } from 'vue-router'
import AppSidebar from '@/components/AppSidebar.vue'
import AppTopBar from '@/components/AppTopBar.vue'
import CommandPalette from '@/components/CommandPalette.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DangerTotpPrompt from '@/components/DangerTotpPrompt.vue'
import ToastHost from '@/components/ToastHost.vue'
import DrawerHost from '@/components/DrawerHost.vue'

/** 演示站的顶部提示。正常构建里 VITE_DEMO 是常量 false，这个组件不会被打包。 */
const DemoBanner = import.meta.env.VITE_DEMO === 'true'
  ? defineAsyncComponent(() => import('@/demo/DemoBanner.vue'))
  : null

const route = useRoute()
const bare = computed(() => route.meta.bare === true)
</script>

<template>
  <template v-if="bare">
    <component :is="DemoBanner" v-if="DemoBanner" />
    <RouterView />
  </template>

  <div v-else class="shell">
    <AppSidebar />
    <div class="shell__main">
      <component :is="DemoBanner" v-if="DemoBanner" />
      <AppTopBar />
      <main class="shell__content">
        <RouterView v-slot="{ Component }">
          <Transition name="view-fade" mode="out-in">
            <component :is="Component" />
          </Transition>
        </RouterView>
      </main>
    </div>
    <DrawerHost />
    <ConfirmDialog />
    <DangerTotpPrompt />
    <CommandPalette />
    <ToastHost />
  </div>
</template>

<style scoped>
.shell { display: flex; height: 100%; overflow: hidden; }
.shell__main { flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; }
.shell__content { flex: 1 1 auto; overflow-y: auto; overflow-x: hidden; }
</style>
